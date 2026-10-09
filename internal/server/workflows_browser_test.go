package server

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"owngit/internal/actions"
	"owngit/internal/state"
	"owngit/internal/testfixture"
	"owngit/internal/webui"
)

func workflowBrowserFixture(t *testing.T) (apiFixture, string, string, *http.Client, *http.Client, string) {
	t.Helper()
	fixture := newAPIFixture(t, false)
	testfixture.WriteWorkflow(t, fixture.work, "ci.yml", "permissions: read-all\n"+testfixture.SurfaceWorkflow+`  unsupported:
    runs-on: ubuntu-latest
    steps:
      - uses: 'other/<img src=x onerror="bad">{action}{0}@v1'
`)
	testfixture.WriteWorkflow(t, fixture.work, "missing.yml", "jobs: {}\n")
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "synthetic workflow")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/main")
	noErr(t, fixture.store.RecordAcceptedActionsPushes(context.Background(), "project", []state.AcceptedActionsPush{{Ref: "refs/heads/main", NewOID: apiGitOutput(t, fixture.work, "rev-parse", "HEAD")}}, time.Now()))
	server, general, jar := openBrowser(t, fixture)
	admin, adminJar := newBrowserClient(t)
	return fixture, server.URL, cookieValue(t, jar, server.URL, generalCookie), general, admin, browserAdminSessionFor(t, fixture, server.URL, adminJar, "wf-admin")
}

func TestWorkflowBrowserTurnOnAndEvents(t *testing.T) {
	fixture, base, generalCSRF, general, admin, csrf := workflowBrowserFixture(t)
	filesURL := base + workflowFilesURL("project")
	review := browserGET(t, general, filesURL)
	if review.status != http.StatusOK || !strings.Contains(review.body, ".github/workflows/ci.yml") || !strings.Contains(review.body, browserText("wf.turnon.first_policy")) {
		t.Fatalf("first-save review status=%d", review.status)
	}
	turnOn := url.Values{"csrf": {generalCSRF}, "action": {webui.ActionTurnOnWorkflows}, "admin_password": {"admin-password"}}
	for _, name := range []string{"review_digest", "base_version", "base_digest"} {
		turnOn.Set(name, formValue(t, review.body, name))
	}
	with := func(name, value string) url.Values {
		values := url.Values{}
		for key, value := range turnOn {
			values[key] = value
		}
		values.Set(name, value)
		return values
	}
	for _, row := range []struct {
		name   string
		values url.Values
		status int
	}{
		{"wrong administrator password", with("admin_password", "wrong"), http.StatusUnauthorized},
		{"no administrator password", with("admin_password", ""), http.StatusUnauthorized},
		{"unknown action", with("action", "enable"), http.StatusBadRequest},
		{"a policy other than the reviewed one", with("review_digest", strings.Repeat("0", 64)), http.StatusConflict},
	} {
		t.Run(row.name, func(t *testing.T) {
			if result := browserForm(t, general, filesURL, row.values, base); result.status != row.status {
				t.Fatalf("status=%d want %d", result.status, row.status)
			}
			if _, exists := storedPolicy(t, fixture); exists {
				t.Fatal("a refused Turn on saved a policy")
			}
		})
	}
	done := browserForm(t, general, filesURL, turnOn, base)
	if done.status != http.StatusSeeOther || done.header.Get("Location") != workflowFilesURL("project")+"?notice=workflow_turned_on" {
		t.Fatalf("Turn on status=%d location=%q", done.status, done.header.Get("Location"))
	}
	if policy, _ := storedPolicy(t, fixture); !policy.RunWorkflows || !policy.ConsentActive || policy.Digest != turnOn.Get("review_digest") {
		t.Fatalf("Turn on did not save the reviewed policy with consent: %+v", policy)
	}
	if notice := noticeRegion(t, browserGET(t, general, base+done.header.Get("Location")).body); !strings.Contains(notice, browserText("wf.notice.turned_on")) {
		t.Fatalf("notice=%q", notice)
	}

	policyURL := base + configuredChecksURL("project")
	for _, row := range []struct {
		name   string
		events []string
		want   []string
	}{
		{"both workflow events are saved", []string{"event_workflow_dispatch", "event_schedule"}, []string{"push", state.ActionsEventSchedule, state.ActionsEventDispatch}},
		{"unchecked events are removed", nil, []string{"push"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			values := validPolicyValues(csrf)
			for _, event := range row.events {
				values.Set(event, "1")
			}
			if result := browserForm(t, admin, policyURL, values, base); result.status != http.StatusSeeOther {
				t.Fatalf("save status=%d", result.status)
			}
			policy, _ := storedPolicy(t, fixture)
			if !slices.Equal(policy.AllowedEvents, row.want) || !policy.RunWorkflows {
				t.Fatalf("events=%v run_workflows=%v", policy.AllowedEvents, policy.RunWorkflows)
			}
			page := browserGET(t, admin, policyURL).body
			for _, event := range []string{"event_workflow_dispatch", "event_schedule"} {
				if checked := strings.Contains(page, `name="`+event+`" value="1" checked`); checked != slices.Contains(row.events, event) {
					t.Fatalf("%s checked=%v after saving %v", event, checked, row.events)
				}
			}
		})
	}
}

func TestWorkflowBrowserRunJourney(t *testing.T) {
	fixture, base, csrf, general, admin, adminCSRF := workflowBrowserFixture(t)
	ctx := context.Background()
	oid := apiGitOutput(t, fixture.work, "rev-parse", "HEAD")
	_, err := fixture.store.SetCheckPolicy(ctx, state.CheckPolicyInput{RepositoryID: "project", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{state.ActionsEventDispatch},
		MaxTimeoutMS: 600000, MaxOutputLimitBytes: 65536, QueueLimit: 16, MaxActiveJobs: 1, MaxLeaseMS: 60000}, time.Now())
	noErr(t, err)
	_, err = fixture.store.GrantCheckConsent(ctx, "project", time.Now())
	noErr(t, err)
	runner, _, _, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "runner", "", time.Now())
	noErr(t, err)

	formURL := base + workflowDispatchURL("project", ".github/workflows/ci.yml", "")
	form := browserGET(t, general, formURL)
	if form.status != http.StatusOK || !strings.Contains(form.body, `name="expected_oid" value="`+oid+`"`) || !strings.Contains(form.body, "anyone who can reach it can start this workflow") {
		t.Fatalf("run form status=%d", form.status)
	}
	dispatch := func(change func(url.Values)) browserHTTPResult {
		values := url.Values{"csrf": {csrf}, "path": {".github/workflows/ci.yml"}, "ref": {"main"}, "expected_oid": {oid}, "input.count": {"3"}, "input.choice": {"second"}}
		change(values)
		return browserForm(t, general, base+workflowFilesURL("project")+"/run", values, base)
	}
	for _, row := range []struct {
		name     string
		change   func(url.Values)
		status   int
		contains string
	}{
		{"the branch moved", func(v url.Values) { v.Set("expected_oid", strings.Repeat("a", 40)) }, http.StatusConflict, "Branch main moved to " + shortOID(oid)},
		{"an unknown input", func(v url.Values) { v.Set("input.other", "1") }, http.StatusUnprocessableEntity, `value="second" selected`},
		{"no commit", func(v url.Values) { v.Del("expected_oid") }, http.StatusUnprocessableEntity, ""},
		{"no CSRF", func(v url.Values) { v.Del("csrf") }, http.StatusForbidden, ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			if result := dispatch(row.change); result.status != row.status || !strings.Contains(result.body, row.contains) {
				t.Fatalf("status=%d want %d", result.status, row.status)
			}
		})
	}
	started := dispatch(func(url.Values) {})
	location := started.header.Get("Location")
	runID, _, _ := strings.Cut(strings.TrimPrefix(location, workflowRunsURL("project")+"/"), "?")
	if started.status != http.StatusSeeOther || !strings.HasSuffix(location, "?notice=workflow_dispatched") || !validAttemptID(runID) {
		t.Fatalf("dispatch status=%d location=%q", started.status, location)
	}
	run, found, err := fixture.store.ActionsRun(ctx, "project", runID)
	if err != nil || !found || run.SourceOID != oid || !strings.Contains(run.InputsJSON, `"count":3`) {
		t.Fatalf("run=%+v found=%v err=%v", run, found, err)
	}

	claimed, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, time.Now(), state.RunnerFeatureWorkflowsV1)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	_, attempt, err := fixture.store.StartCheckJob(ctx, state.CheckJobStart{RepositoryID: "project", JobID: claimed.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID,
		CredentialGeneration: runner.Generation, AttemptID: strings.Repeat("c", 32), Protection: state.ProtectionRunnerReported}, time.Now())
	noErr(t, err)
	exit := 1
	_, _, err = fixture.store.CompleteCheckJobAttempt(ctx, state.CheckCompletion{AttemptID: attempt.ID, RepositoryID: "project", TaskID: attempt.TaskID, FinishedAt: time.Now(),
		WorktreeState: state.WorktreeClean, Log: "synthetic log line\n", Results: []state.CheckResult{{Name: attempt.Checks[0].Name, Command: attempt.Checks[0].Command,
			Status: state.AttemptFailed, Role: actions.RoleRun, ExitCode: &exit, DurationMS: 5, OutputExcerpt: "synthetic excerpt ***"}}},
		state.CheckJobCompletionAuthority{JobID: claimed.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation}, time.Now())
	noErr(t, err)
	jobs, err := fixture.store.ActionsRunJobs(ctx, "project", runID)
	noErr(t, err)
	var waiting string
	for _, job := range jobs {
		if job.ID != claimed.ID {
			waiting = job.ID
		}
	}

	runURL := workflowRunURL("project", runID)
	jobURL := workflowJobURL("project", runID, claimed.ID)
	unknown := strings.Repeat("0", 32)
	for _, row := range []struct {
		name, path   string
		status       int
		contains     []string
		header       string
		withoutValue bool
	}{
		{name: "discovery carries message values", path: workflowFilesURL("project") + "?lang=ko", status: 200, contains: []string{`data-en="permissions is not applied: OwnGit gives workflows no GitHub token."`, "permissions는 적용되지 않습니다: OwnGit은 워크플로에 GitHub 토큰을 주지 않습니다.</span>", "other/&lt;img src=x onerror=&#34;bad&#34;&gt;{action}{0}@v1", "on (line 0) must be an event name, list or mapping."}},
		{name: "run facts carry message values", path: runURL + "?lang=ko", status: 200, contains: []string{"permissions는 적용되지 않습니다: OwnGit은 워크플로에 GitHub 토큰을 주지 않습니다.</span>", "other/&lt;img src=x onerror=&#34;bad&#34;&gt;{action}{0}@v1은 실행되지 않습니다."}},
		{name: "the run list links the run", path: workflowRunsURL("project"), status: 200, contains: []string{runURL, ".github/workflows/ci.yml"}},
		{name: "the run shows jobs, inputs and the secret it names", path: runURL, status: 200, contains: []string{jobURL, "build", "count", "TOKEN", browserText("wf.secret.not_set")}},
		{name: "the dispatch notice", path: location, status: 200, contains: []string{browserText("wf.notice.dispatched")}},
		{name: "the job shows the command, excerpt and log link", path: jobURL, status: 200, contains: []string{"echo synthetic", "synthetic excerpt ***", jobURL + "/log"}},
		{name: "the raw log", path: jobURL + "/log", status: 200, contains: []string{"synthetic log line"}, header: "text/plain; charset=utf-8"},
		{name: "a job that never ran has no log", path: workflowJobURL("project", runID, waiting) + "/log", status: 404},
		{name: "an unknown run", path: workflowRunURL("project", unknown), status: 404, contains: []string{browserText("wf.not_found")}},
		{name: "a job of another run", path: workflowJobURL("project", unknown, claimed.ID), status: 404},
		{name: "a file that cannot be run by hand", path: workflowDispatchURL("project", ".github/workflows/none.yml", ""), status: 404},
		{name: "the overview counts workflows and links the runs", path: "/repositories/project", status: 200, contains: []string{`href="` + workflowRunsURL("project") + `"`, "1 workflow"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			result := browserGET(t, general, base+row.path)
			if result.status != row.status || row.header != "" && (result.header.Get("Content-Type") != row.header || result.header.Get("X-Content-Type-Options") != "nosniff") {
				t.Fatalf("status=%d type=%q", result.status, result.header.Get("Content-Type"))
			}
			for _, want := range row.contains {
				if !strings.Contains(result.body, want) {
					t.Fatalf("missing %q", want)
				}
			}
		})
	}

	for _, row := range []struct{ action, notice string }{{"cancel", "workflow_cancel"}, {"rerun", "workflow_rerun"}, {"rerun", "workflow_rerun_existing"}} {
		t.Run(row.notice, func(t *testing.T) {
			result := browserForm(t, general, base+runURL+"/"+row.action, url.Values{"csrf": {csrf}}, base)
			next := result.header.Get("Location")
			if result.status != http.StatusSeeOther || !strings.HasPrefix(next, workflowRunsURL("project")+"/") || !strings.HasSuffix(next, "?notice="+row.notice) {
				t.Fatalf("status=%d location=%q", result.status, result.header.Get("Location"))
			}
		})
	}

	secretsURL := base + workflowSecretsURL("project")
	if result := browserGET(t, general, secretsURL); result.status != http.StatusSeeOther || !strings.HasPrefix(result.header.Get("Location"), "/admin/login") {
		t.Fatalf("general access to secrets status=%d", result.status)
	}
	secret := func(action, name, password string) browserHTTPResult {
		return browserForm(t, admin, secretsURL, url.Values{"csrf": {adminCSRF}, "action": {action}, "name": {name}, "value": {"synthetic-private-value"}, "admin_password": {password}}, base)
	}
	listed := func(page string) bool { return strings.Contains(page, `?remove=TOKEN#wf-secret-remove"`) }
	for _, row := range []struct {
		name, action, secret, password string
		status                         int
		listed                         bool
	}{
		{"reserved name", webui.ActionSetSecret, "GITHUB_TOKEN", "admin-password", http.StatusUnprocessableEntity, false},
		{"set", webui.ActionSetSecret, "TOKEN", "admin-password", http.StatusSeeOther, true},
		{"remove", webui.ActionRemoveSecret, "TOKEN", "admin-password", http.StatusSeeOther, false},
	} {
		t.Run("secret "+row.name, func(t *testing.T) {
			if row.action == webui.ActionRemoveSecret {
				confirm := browserGET(t, admin, secretsURL+"?remove=TOKEN")
				if confirm.status != http.StatusOK || !strings.Contains(confirm.body, "Remove TOKEN?") || !strings.Contains(confirm.body, `name="action" value="remove"`) {
					t.Fatalf("confirm step status=%d", confirm.status)
				}
				if !listed(browserGET(t, admin, secretsURL).body) {
					t.Fatal("opening the removal step removed the secret")
				}
			}
			if result := secret(row.action, row.secret, row.password); result.status != row.status || strings.Contains(result.body, "synthetic-private-value") {
				t.Fatalf("status=%d want %d", result.status, row.status)
			}
			page := browserGET(t, admin, secretsURL).body
			if strings.Contains(page, "synthetic-private-value") || listed(page) != row.listed {
				t.Fatalf("listed=%v want %v", !row.listed, row.listed)
			}
		})
	}
}
