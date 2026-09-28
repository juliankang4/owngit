package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// loggedFailures returns the failures, unavailable or internal, logged after
// offset since. The failures counted so far are reported first, so a failure
// logged twice shows as its line and a count line, not as one line.
func loggedFailures(serverLog *lockedLog, since int) []string {
	failures.flush()
	var lines []string
	for _, line := range strings.Split(serverLog.String()[since:], "\n") {
		if strings.Contains(line, "could not be completed") {
			lines = append(lines, line)
		}
	}
	return lines
}

// checkLoggedSteps checks that lines are exactly one line for each step.
func checkLoggedSteps(t *testing.T, what string, lines []string, steps ...string) {
	t.Helper()
	logged := strings.Join(lines, "\n")
	if len(lines) != len(steps) {
		t.Errorf("%s logged %d lines, want one for each of %q:\n%s", what, len(lines), steps, logged)
		return
	}
	for _, step := range steps {
		if strings.Count(logged, ": "+step+" could not be completed: ") != 1 {
			t.Errorf("%s logged no line for %q:\n%s", what, step, logged)
		}
	}
}

// hideTable makes every statement on table fail, as a damaged state
// database does, until the returned function puts it back.
func hideTable(t *testing.T, store *state.Store, table string) func() {
	t.Helper()
	noErr(t, store.Exec(context.Background(), `ALTER TABLE `+table+` RENAME TO hidden_`+table))
	return func() {
		t.Helper()
		noErr(t, store.Exec(context.Background(), `ALTER TABLE hidden_`+table+` RENAME TO `+table))
	}
}

// A pull request comparison, a restore preview and a restore that failed in
// Git are each logged once with their cause.
func TestPullRequestAndRestoreFailuresLogTheirCauseOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing Git wrapper is a Unix test fixture")
	}
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	if _, err := fixture.app.PullRequests.Create(ctx, pullrequest.CreateInput{
		Repository: "project", Title: "Logged", SourceBranch: "feature", TargetBranch: "main",
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID, ReviewChoice: webui.ReviewChoiceRequest,
	}); err != nil {
		t.Fatal(err)
	}
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	base := server.URL + "/repositories/project"
	if _, status := dashboardGET(t, client, base); status != http.StatusOK {
		t.Fatalf("overview status=%d", status)
	}
	csrf := cookieValue(t, jar, server.URL, generalCookie)
	all := repository.RestoreRequest{Source: fixture.sourceOID, Target: "main", Mode: repository.RestoreAll}
	preview, err := fixture.app.Repositories.PreviewRestore(ctx, "project", all)
	noErr(t, err)
	serverLog := captureServerLog(t)

	// The comparisons come first, so no cached comparison answers them. A
	// restore of selected files writes a tree, which the whole-tree preview
	// the page also shows does not; creating the restore commit is the
	// first step only an apply takes.
	for _, check := range []struct {
		pattern, path string
		form          url.Values
		step          string
	}{
		{"merge-base", "/pull-requests/new?source=feature&target=main", nil, "pull request comparison"},
		{"merge-base", "/pull-requests/1", nil, "pull request comparison"},
		{"write-tree", "/restore/preview", url.Values{
			"csrf": {csrf}, "source": {fixture.sourceOID}, "target": {"main"}, "mode": {"files"}, "path": {"feature.txt"},
		}, "restore preview"},
		{"commit-tree", "/restore", url.Values{
			"csrf": {csrf}, "source": {fixture.sourceOID}, "target": {"main"}, "mode": {"all"},
			"expected_head": {preview.ExpectedHead}, "confirm": {"restore"},
		}, "restore apply"},
	} {
		endFailureWindows()
		since := len(serverLog.String())
		failPath := failGitWhile(t, fixture.app, check.pattern)
		noErr(t, os.WriteFile(failPath, nil, 0o600))
		var status int
		if check.form == nil {
			_, status = dashboardGET(t, client, base+check.path)
		} else {
			status = browserForm(t, client, base+check.path, check.form, server.URL).status
		}
		noErr(t, os.Remove(failPath))
		what := check.path + " with failing " + check.pattern
		if status != http.StatusServiceUnavailable {
			t.Errorf("%s status=%d, want 503", what, status)
		}
		checkLoggedSteps(t, what, loggedFailures(serverLog, since), check.step)
	}
}

// Reads and writes of OwnGit's state behind a repository page that fail are
// answered as unavailable and logged once each. A repository record that
// could not be read is never answered as a repository that does not exist.
func TestStateFailuresBehindRepositoryPagesAreLogged(t *testing.T) {
	fixture := newAPIFixture(t, false)
	ctx := context.Background()
	if _, err := fixture.app.PullRequests.Create(ctx, pullrequest.CreateInput{
		Repository: "project", Title: "Logged", SourceBranch: "feature", TargetBranch: "main",
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID, ReviewChoice: webui.ReviewChoiceRequest,
	}); err != nil {
		t.Fatal(err)
	}
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	base := server.URL + "/repositories/project"
	if _, status := dashboardGET(t, client, base+"/pull-requests/1"); status != http.StatusOK {
		t.Fatalf("pull request status=%d", status)
	}
	csrf := cookieValue(t, jar, server.URL, generalCookie)
	adminCSRF := browserAdminSessionFor(t, fixture, server.URL, jar, "logged-admin")
	serverLog := captureServerLog(t)
	get := func(path string) int {
		_, status := dashboardGET(t, client, server.URL+path)
		return status
	}
	post := func(path string, form url.Values) func() int {
		return func() int { return browserForm(t, client, server.URL+path, form, server.URL).status }
	}
	hidden := func(table string, path string) func() int {
		return func() int {
			restore := hideTable(t, fixture.store, table)
			defer restore()
			return get(path)
		}
	}
	refused := func(table, event string, send func() int) func() int {
		return func() int {
			refuseWrites(t, fixture.store, "refuse_logged", event+" ON "+table)
			defer func() { noErr(t, fixture.store.Exec(ctx, `DROP TRIGGER refuse_logged`)) }()
			return send()
		}
	}
	for _, check := range []struct {
		what  string
		send  func() int
		steps []string
	}{
		{"settings", hidden("metadata", "/"), []string{"settings read"}},
		{"repository list", hidden("repositories", "/"), []string{"page frame read"}},
		// The error page after the failed record read shows no frame, since
		// its repository list cannot be read either.
		{"repository record", hidden("repositories", "/repositories/project"), []string{"repository record read", "page frame read"}},
		{"archive API repository record", hidden("repositories", "/api/v1/repositories/project/archive?format=zip"), []string{"repository record read"}},
		{"pull request list", hidden("pull_requests", "/repositories/project/pull-requests"), []string{"pull request list read"}},
		{"pull request", hidden("pull_requests", "/repositories/project/pull-requests/1"), []string{"pull request read"}},
		{"pull request close", refused("pull_requests", "UPDATE", post("/repositories/project/pull-requests/1/close", url.Values{"csrf": {csrf}})), []string{"pull request close"}},
		{"repository creation", refused("repositories", "INSERT", post("/repositories", url.Values{"csrf": {csrf}, "name": {"fresh"}})), []string{"repository creation"}},
		{"configured check policy save", refused("check_policies", "INSERT", post(configuredChecksURL("project"), validPolicyValues(adminCSRF))), []string{"configured check policy save"}},
		{"configured check consent change", func() int {
			if status := post(configuredChecksURL("project"), validPolicyValues(adminCSRF))(); status != http.StatusSeeOther {
				t.Fatalf("policy save status=%d", status)
			}
			policy, _, err := fixture.store.CheckPolicy(ctx, "project")
			noErr(t, err)
			return refused("check_policies", "UPDATE", post(configuredChecksURL("project"), url.Values{
				"csrf": {adminCSRF}, "action": {webui.ActionEnableChecks}, "admin_password": {"admin-password"},
				"policy_version": {strconv.FormatInt(policy.Version, 10)}, "policy_digest": {policy.Digest},
			}))()
		}, []string{"configured check consent change"}},
	} {
		endFailureWindows()
		since := len(serverLog.String())
		if status := check.send(); status != http.StatusServiceUnavailable {
			t.Errorf("%s status=%d, want 503", check.what, status)
		}
		checkLoggedSteps(t, check.what, loggedFailures(serverLog, since), check.steps...)
	}

	// With the state readable again the same pages log nothing.
	endFailureWindows()
	since := len(serverLog.String())
	for _, path := range []string{"/", "/repositories/project", "/repositories/project/pull-requests", "/repositories/project/pull-requests/1"} {
		if status := get(path); status != http.StatusOK {
			t.Errorf("GET %s after the state recovered status=%d, want 200", path, status)
		}
	}
	checkLoggedSteps(t, "pages that could be read", loggedFailures(serverLog, since))
}

// A language count that failed is logged with its cause. It has its own
// repository, since the overview reads the languages before anything else.
func TestLanguageCountFailureIsLogged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing Git wrapper is a Unix test fixture")
	}
	app := newConfiguredApp(t)
	readFailureRepository(t, app, "languages")
	server := serve(t, app.Handler())
	client, _ := newBrowserClient(t)
	serverLog := captureServerLog(t)
	// Only the language count lists the whole tree recursively.
	failPath := failGitWhile(t, app, "-r")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	body, status := dashboardGET(t, client, server.URL+"/repositories/languages")
	if status != http.StatusOK || !strings.Contains(body, webui.Text(webui.LangEN, webui.MsgRepoLanguagesUnavailable)) {
		t.Errorf("overview with a failing language count status=%d, want 200 with the panel saying so", status)
	}
	checkLoggedSteps(t, "failing language count", loggedFailures(serverLog, 0), "language count")
}

// The pull request list says what its status says: a fault in OwnGit for
// 500, which waiting does not fix, unavailable for 503, and for 413 that the
// list is too long to show.
func TestPullRequestListTextFollowsItsStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing wrapper is a Unix test fixture")
	}
	internal, unavailable := "Something went wrong on the server.", "This is temporarily unavailable."
	tooLong := "more pull requests than the list can show"
	for _, check := range []struct {
		what, wrapper string
		// rows are closed pull requests recorded beside the open one.
		rows   int
		status int
		text   string
		logged []string
	}{
		{"an invalid branch head", `for a in "$@"; do if test "$a" = for-each-ref; then printf 'refs/heads/main\000nothex\000commit\n'; exit 0; fi; done`, 0,
			http.StatusInternalServerError, internal, []string{"pull request list read"}},
		{"branch heads that could not be read", `for a in "$@"; do if test "$a" = for-each-ref; then echo 'fatal: simulated storage failure' >&2; exit 128; fi; done`, 0,
			http.StatusServiceUnavailable, unavailable, []string{"pull request list read"}},
		{"a list longer than one answer", "", pullrequest.MaximumListResults, http.StatusRequestEntityTooLarge, tooLong, nil},
	} {
		fixture := newAPIFixture(t, false)
		_, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
			Repository: "project", Title: "Listed", SourceBranch: "feature", TargetBranch: "main",
			SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID,
		})
		noErr(t, err)
		if check.rows != 0 {
			noErr(t, fixture.store.Exec(context.Background(), `INSERT INTO pull_requests(repository_id,number,title,source_branch,target_branch,status,created_at,updated_at)
				WITH RECURSIVE n(i) AS (SELECT 2 UNION ALL SELECT i+1 FROM n WHERE i<?)
				SELECT 'project',i,'Closed','feature','main','closed',1,1 FROM n`, check.rows+1))
		}
		server, client, _ := openBrowser(t, fixture)
		if check.wrapper != "" {
			useGitWrapper(t, fixture.app, check.wrapper)
		}
		serverLog := captureServerLog(t)
		result := browserGET(t, client, server.URL+"/repositories/project/pull-requests")
		if result.status != check.status || !strings.Contains(result.body, check.text) {
			t.Errorf("%s: status=%d, want %d with %q", check.what, result.status, check.status, check.text)
		}
		for _, other := range []string{internal, unavailable, tooLong} {
			if other != check.text && strings.Contains(result.body, other) {
				t.Errorf("%s: the page also says %q", check.what, other)
			}
		}
		checkLoggedSteps(t, check.what, loggedFailures(serverLog, 0), check.logged...)
	}
}
