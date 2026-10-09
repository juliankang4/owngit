package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/actions"
	"owngit/internal/checkapi"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/testfixture"
	"owngit/internal/workflows"
)

func TestWorkflowDiscoveryRefusedNames(t *testing.T) {
	fixture := newAPIFixture(t, false)
	cases := []struct {
		name, limit, detail string
	}{
		{strings.Repeat("a", 97) + ".yml", "100 bytes", "Workflow filename exceeds 100 bytes."},
	}
	if runtime.GOOS != "windows" {
		cases = append(cases, struct{ name, limit, detail string }{"bad\tname.yml", "", "Workflow filename cannot be stored as a run identity."})
	}
	for _, tc := range cases {
		testfixture.WriteWorkflow(t, fixture.work, tc.name, testfixture.SurfaceWorkflow)
	}
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "synthetic refused workflow names")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/main")
	result, err := (workflows.Service{Store: fixture.store, Repositories: fixture.app.Repositories}).Discover(context.Background(), "project", "main")
	noErr(t, err)
	for _, tc := range cases {
		path := ".github/workflows/" + tc.name
		found := false
		for _, file := range result.Workflows {
			if file.Path != path {
				continue
			}
			found = true
			if file.Refusal == nil || file.Refusal.Code != "workflow.limit" || file.Refusal.Detail != tc.detail {
				t.Fatalf("discovery refusal for %q: %+v", path, file.Refusal)
			}
			if tc.limit == "" {
				if file.Refusal.Args != nil {
					t.Fatalf("unsupported name has limit args: %+v", file.Refusal)
				}
			} else if file.Refusal.Args["what"] != path || file.Refusal.Args["limit"] != tc.limit || len(file.Refusal.Args) != 2 {
				t.Fatalf("discovery limit args for %q: %+v", path, file.Refusal)
			}
		}
		if !found {
			t.Fatalf("missing discovery for %q: %+v", path, result.Workflows)
		}
	}
}

func TestWorkflowAPI(t *testing.T) {
	fixture := newAPIFixture(t, true)
	ctx := context.Background()
	noErr(t, fixture.store.SavePolicies(ctx, state.PolicyChange{LoginLimits: &state.LoginLimits{Attempts: 100, Window: time.Minute, Pause: time.Minute}}))
	testfixture.WriteWorkflow(t, fixture.work, "ci.yml", testfixture.SurfaceWorkflow)
	testfixture.WriteWorkflow(t, fixture.work, "dynamic.yml", `on:
  workflow_dispatch:
    inputs:
      axes:
        type: string
        required: true
jobs:
  dynamic:
    runs-on: ubuntu-latest
    strategy:
      matrix: ${{ fromJSON(inputs.axes) }}
    steps:
      - run: echo dynamic
`)
	testfixture.WriteWorkflow(t, fixture.work, "scheduled.yml", `on:
  schedule:
    - cron: '* * * * *'
jobs:
  timed:
    runs-on: ubuntu-latest
    steps:
      - run: echo scheduled
`)
	testfixture.WriteWorkflow(t, fixture.work, "unsupported.yml", "on: issues\njobs:\n  ignored:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo unsupported\n")
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "synthetic workflows")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/main")
	oid := apiGitOutput(t, fixture.work, "rev-parse", "HEAD")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/imported")
	noErr(t, fixture.store.RecordAcceptedActionsPushes(ctx, "project", []state.AcceptedActionsPush{{Ref: "refs/heads/main", NewOID: oid}}, time.Now()))
	policy := state.CheckPolicyInput{RepositoryID: "project", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{state.ActionsEventDispatch, "pull_request", state.ActionsEventSchedule}, MaxTimeoutMS: 600000, MaxOutputLimitBytes: 65536, QueueLimit: 16, MaxActiveJobs: 1, MaxLeaseMS: 60000}
	savedPolicy, err := fixture.store.SetCheckPolicy(ctx, policy, time.Now())
	noErr(t, err)
	_, err = fixture.store.GrantCheckConsent(ctx, "project", time.Now())
	noErr(t, err)
	_, err = fixture.store.RebuildActionsSchedules(ctx, "project", "refs/heads/main", oid, state.ExpectedCheckPolicy{Version: savedPolicy.Version, Digest: savedPolicy.Digest}, []state.ActionsSchedule{{WorkflowPath: ".github/workflows/scheduled.yml", Cron: "* * * * *"}}, time.Now())
	noErr(t, err)
	httpServer := httptest.NewServer(fixture.app.Handler())
	t.Cleanup(httpServer.Close)
	base := httpServer.URL + "/api/v1/repositories/project"
	dispatch := workflows.DispatchInput{Path: ".github/workflows/ci.yml", ExpectedOID: oid}
	response := apiRequest(t, http.MethodPost, base+"/workflows/dispatch", dispatch, "shared-password", "")
	var admitted workflowRunResponse
	noErr(t, json.NewDecoder(response.Body).Decode(&admitted))
	noErr(t, response.Body.Close())
	if response.StatusCode != 200 || admitted.Run.Conclusion != "queued" || len(admitted.Jobs) != 2 || admitted.Run.Actor.Kind != state.ActorAccess {
		t.Fatalf("dispatch status=%d result=%+v", response.StatusCode, admitted)
	}
	root := admitted.Run.ID
	job := admitted.Jobs[0].Job.ID
	if _, found, err := fixture.store.ClaimCheckJob(ctx, "project", mustWorkflowRunner(t, fixture.store), time.Now()); err != nil || found {
		t.Fatalf("old runner claim found=%v error=%v", found, err)
	}
	refusedRuns := map[string]state.ActionsRun{}
	for _, code := range []string{"workflow.limit", "workflow.invalid"} {
		run, _, err := fixture.store.AdmitActionsRun(ctx, state.ActionsRunRequest{Run: state.ActionsRun{RepositoryID: "project", WorkflowPath: ".github/workflows/" + strings.TrimPrefix(code, "workflow.") + ".yml", SourceOID: oid, TriggerRef: "main", Event: state.ActionsEventDispatch, EventKey: "dispatch/" + code, Outcome: "refused", Reason: "Synthetic per-file refusal.", Actor: state.Actor{Kind: state.ActorAccess}, Facts: actions.RunFacts{Notes: []actions.Message{{Code: code, Detail: "Synthetic per-file refusal."}}}}}, time.Now())
		noErr(t, err)
		refusedRuns[code] = run
	}
	rows := []struct {
		name, method, path, password, code, contains, excludes string
		body                                                   any
		status                                                 int
	}{
		{name: "list without password", method: "GET", path: "/workflows", status: 401, code: "authentication_required"},
		{name: "general list", method: "GET", path: "/workflows", password: "shared-password", status: 200, contains: "workflow.event"},
		{name: "general show", method: "GET", path: "/workflows?path=.github%2Fworkflows%2Fci.yml", password: "shared-password", status: 200, contains: "workflow_dispatch"},
		{name: "dynamic input preview", method: "GET", path: "/workflows?path=.github%2Fworkflows%2Fdynamic.yml", password: "shared-password", status: 200, contains: `"preview_requires_inputs":true`, excludes: `"refusal"`},
		{name: "workflow limit note", method: "GET", path: "/workflow-runs/" + refusedRuns["workflow.limit"].ID, password: "shared-password", status: 200, contains: `"code":"workflow.limit"`},
		{name: "workflow invalid note", method: "GET", path: "/workflow-runs/" + refusedRuns["workflow.invalid"].ID, password: "shared-password", status: 200, contains: `"code":"workflow.invalid"`},
		{name: "read-only schedule timing and authority note", method: "GET", path: "/workflows?path=.github%2Fworkflows%2Fscheduled.yml", password: "shared-password", status: 200, contains: `"next_due_at"`},
		{name: "general runs use compact summaries", method: "GET", path: "/workflow-runs", password: "shared-password", status: 200, contains: `"counts"`, excludes: `"facts"`},
		{name: "general run with old runner note", method: "GET", path: "/workflow-runs/" + root, password: "shared-password", status: 200, contains: "note.runner_old"},
		{name: "unknown run", method: "GET", path: "/workflow-runs/" + strings.Repeat("0", 32), password: "shared-password", status: 404, code: "workflow.run_not_found"},
		{name: "unknown job", method: "GET", path: "/workflow-runs/" + root + "/jobs/" + strings.Repeat("0", 32) + "/log", password: "shared-password", status: 404, code: "workflow.job_not_found"},
		{name: "job details include commands", method: "GET", path: "/workflow-runs/" + root + "/jobs/" + job, password: "shared-password", status: 200, contains: "echo synthetic"},
		{name: "unknown job details", method: "GET", path: "/workflow-runs/" + root + "/jobs/" + strings.Repeat("0", 32), password: "shared-password", status: 404, code: "workflow.job_not_found"},
		{name: "nothing ran has no raw log", method: "GET", path: "/workflow-runs/" + root + "/jobs/" + job + "/log", password: "shared-password", status: 404, code: "check_log_missing"},
		{name: "dispatch refuses administrator as shared password", method: "POST", path: "/workflows/dispatch", password: "admin-password", body: dispatch, status: 401},
		{name: "moved branch", method: "POST", path: "/workflows/dispatch", password: "shared-password", body: workflows.DispatchInput{Path: dispatch.Path, ExpectedOID: strings.Repeat("a", 40)}, status: 409, code: "workflow.moved"},
		{name: "imported branch with accepted source on another ref", method: "POST", path: "/workflows/dispatch", password: "shared-password", body: workflows.DispatchInput{Path: dispatch.Path, Ref: "imported", ExpectedOID: oid}, status: 409, code: "note.push_required"},
		{name: "bad input", method: "POST", path: "/workflows/dispatch", password: "shared-password", body: workflows.DispatchInput{Path: dispatch.Path, Inputs: map[string]any{"unknown": "value"}}, status: 422, code: "workflow.dispatch_input"},
		{name: "unsupported dispatch event", method: "POST", path: "/workflows/dispatch", password: "shared-password", body: workflows.DispatchInput{Path: ".github/workflows/unsupported.yml"}, status: 409, code: "workflow.event_off"},
		{name: "cancel needs password", method: "POST", path: "/workflow-runs/" + root + "/cancel", body: struct{}{}, status: 401},
		{name: "cancel requires JSON", method: "POST", path: "/workflow-runs/" + root + "/cancel", password: "shared-password", status: 415},
		{name: "general cancel", method: "POST", path: "/workflow-runs/" + root + "/cancel", password: "shared-password", body: struct{}{}, status: 200, contains: "cancelled"},
		{name: "general rerun", method: "POST", path: "/workflow-runs/" + root + "/rerun", password: "shared-password", body: struct{}{}, status: 200, contains: "rerun_generation"},
		{name: "rerun deduplicates", method: "POST", path: "/workflow-runs/" + root + "/rerun", password: "shared-password", body: struct{}{}, status: 200, contains: "deduplicated"},
		{name: "general secret list refused", method: "GET", path: "/workflow-secrets", password: "shared-password", status: 401, code: "admin_authentication_required"},
		{name: "general secret set refused", method: "PUT", path: "/workflow-secrets/TOKEN", password: "shared-password", body: map[string]string{"value": "synthetic-private-value"}, status: 401},
		{name: "admin secret set", method: "PUT", path: "/workflow-secrets/TOKEN", password: "admin-password", body: map[string]string{"value": "synthetic-private-value"}, status: 200, contains: "updated_at"},
		{name: "admin secret list", method: "GET", path: "/workflow-secrets", password: "admin-password", status: 200, contains: "TOKEN"},
		{name: "run shows names and presence only", method: "GET", path: "/workflow-runs/" + root, password: "shared-password", status: 200, contains: "\"set\":true"},
		{name: "bad secret name", method: "PUT", path: "/workflow-secrets/GITHUB_TOKEN", password: "admin-password", body: map[string]string{"value": "synthetic-private-value"}, status: 422, code: "workflow.secret_invalid"},
		{name: "secret remove", method: "DELETE", path: "/workflow-secrets/TOKEN", password: "admin-password", body: struct{}{}, status: 200, contains: "ok"},
		{name: "unknown query", method: "GET", path: "/workflows?command=echo", password: "shared-password", status: 400, code: "invalid_request"},
		{name: "duplicate query", method: "GET", path: "/workflows?ref=main&ref=feature", password: "shared-password", status: 400, code: "invalid_request"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if row.name == "general rerun" {
				noErr(t, fixture.store.Exec(ctx, `DELETE FROM actions_accepted_pushes WHERE repository_id=?`, "project"))
			}
			user := "owngit"
			if strings.HasPrefix(row.path, "/workflow-secrets") && row.password == "admin-password" {
				user = "admin"
			}
			answer := sendJSON(t, row.method, base+row.path, row.body, basicAuth(user, row.password))
			content, err := io.ReadAll(answer.Body)
			noErr(t, err)
			noErr(t, answer.Body.Close())
			if answer.StatusCode != row.status || row.code != "" && !strings.Contains(string(content), `"code":"`+row.code+`"`) || row.contains != "" && !strings.Contains(string(content), row.contains) || row.excludes != "" && strings.Contains(string(content), row.excludes) {
				t.Fatalf("status=%d body=%s", answer.StatusCode, content)
			}
			if strings.Contains(string(content), "synthetic-private-value") || strings.Contains(string(content), `"value"`) {
				t.Fatalf("secret value returned: %s", content)
			}
		})
	}
	t.Run("pinned busy is an explicit pre-admission refusal", func(t *testing.T) {
		writer := httptest.NewRecorder()
		request := httptest.NewRequest("POST", base+"/workflows/dispatch", nil)
		writeWorkflowError(writer, request, repository.ErrPinnedRepositoryBusy)
		if writer.Code != 503 || !strings.Contains(writer.Body.String(), `"code":"repository_busy"`) {
			t.Fatalf("busy response=%d %s", writer.Code, writer.Body.String())
		}
	})
	created := createShare(t, httpServer.URL, "project", map[string]any{"label": "Synthetic viewer"})
	shareClient, _ := openShare(t, created)
	for _, path := range []string{"/workflows/dispatch", "/workflow-runs/" + root + "/cancel", "/workflow-runs/" + root + "/rerun"} {
		request, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(`{}`))
		noErr(t, err)
		request.Header.Set("Content-Type", "application/json")
		answer, err := shareClient.Do(request)
		noErr(t, err)
		if answer.StatusCode != 401 {
			t.Fatalf("share access to %s: %d", path, answer.StatusCode)
		}
		noErr(t, answer.Body.Close())
	}
	helperBase, token := helperAPI(t, fixture, "workflow summary", time.Now())
	taskID := admitted.Jobs[0].Job.TaskID
	for _, row := range []struct {
		name, path string
		request    func(string) *http.Response
	}{
		{name: "helper task list", path: "/tasks", request: func(path string) *http.Response { return checkRequest(t, "GET", helperBase+path, nil, token) }},
		{name: "helper task detail", path: "/tasks/" + taskID, request: func(path string) *http.Response { return checkRequest(t, "GET", helperBase+path, nil, token) }},
		{name: "general task list", path: taskViewAPIPath + "/project", request: func(path string) *http.Response {
			return apiRequest(t, "GET", httpServer.URL+path, nil, "shared-password", "")
		}},
		{name: "general task detail", path: taskViewAPIPath + "/project/" + taskID, request: func(path string) *http.Response {
			return apiRequest(t, "GET", httpServer.URL+path, nil, "shared-password", "")
		}},
	} {
		t.Run(row.name+" keeps lifecycle", func(t *testing.T) {
			answer := row.request(row.path)
			content, err := io.ReadAll(answer.Body)
			noErr(t, err)
			noErr(t, answer.Body.Close())
			if answer.StatusCode != 200 || !strings.Contains(string(content), `"status":"active"`) || !strings.Contains(string(content), `"conclusion":"queued"`) {
				t.Fatalf("evidence %s: %d %s", row.path, answer.StatusCode, content)
			}
		})
	}
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/feature")
	pr, err := fixture.store.CreatePullRequest(ctx, "project", "Synthetic workflow change", "feature", "main", oid, fixture.targetOID, state.ReviewNotRequested, time.Now())
	noErr(t, err)
	answer := apiRequest(t, "GET", base+"/pull-requests/"+itoa(pr.Number), nil, "shared-password", "")
	content, err := io.ReadAll(answer.Body)
	noErr(t, err)
	noErr(t, answer.Body.Close())
	if answer.StatusCode != 200 || !strings.Contains(string(content), `"conclusion":"queued"`) || !strings.Contains(string(content), `"code":"note.push_required"`) {
		t.Fatalf("PR evidence: %d %s", answer.StatusCode, content)
	}
	wire := checkapi.PolicyInput{Executor: policy.Executor, AllowedEvents: policy.AllowedEvents, MaxTimeoutMS: policy.MaxTimeoutMS, MaxOutputLimitBytes: policy.MaxOutputLimitBytes, QueueLimit: policy.QueueLimit, MaxActiveJobs: policy.MaxActiveJobs, MaxLeaseMS: policy.MaxLeaseMS, Execution: policy.Execution, RunWorkflows: new(false)}
	saved := adminAPIRequest(t, "PUT", base+"/check-policy", wire, "admin-password")
	var offAnswer checkapi.PolicyResponse
	decodeCheckJSON(t, saved, &offAnswer)
	if offAnswer.Policy == nil || offAnswer.Policy.RunWorkflows || offAnswer.Policy.ConsentActive {
		t.Fatalf("explicit off did not round trip: %+v", offAnswer)
	}
	answer = apiRequest(t, "POST", base+"/workflows/dispatch", dispatch, "shared-password", "")
	if code := apiErrorCode(t, answer); code != "workflow.off" {
		t.Fatalf("off=%s", code)
	}
	wire.RunWorkflows = new(true)
	saved = adminAPIRequest(t, "POST", base+"/check-policy/save-and-enable", checkapi.SaveAndEnableInput{Policy: wire}, "admin-password")
	var onAnswer checkapi.PolicyResponse
	decodeCheckJSON(t, saved, &onAnswer)
	if onAnswer.Policy == nil || !onAnswer.Policy.RunWorkflows || !onAnswer.Policy.ConsentActive {
		t.Fatalf("Turn on did not bind fresh consent: %+v", onAnswer)
	}
}

func mustWorkflowRunner(t *testing.T, store *state.Store) string {
	t.Helper()
	credential, _, _, err := store.IssueCheckRunnerToken(context.Background(), "project", "old", "", time.Now())
	noErr(t, err)
	return credential.ID
}
