package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/testfixture"
	"owngit/internal/webui"
)

// refuseWrites makes every statement matching event, such as
// "DELETE ON login_attempts", fail until the trigger is dropped.
func refuseWrites(t *testing.T, store *state.Store, trigger, event string) {
	t.Helper()
	noErr(t, store.Exec(context.Background(), `CREATE TRIGGER `+trigger+` BEFORE `+event+` BEGIN SELECT RAISE(ABORT, 'injected failure'); END`))
}

// endFailureWindows reports the failures counted so far and ends their
// windows, so the next request logs its own line even when an earlier
// request of the test failed the same way. A test that checks which request
// logged a failure calls it between such requests.
func endFailureWindows() { failures.flush() }

// captureServerLog sends the standard logger to a buffer for this test.
// Failures counted before are reported to the earlier output first, so this
// test's first failure of each kind is logged, and the failures this test
// counted are reported into its buffer when it ends.
func captureServerLog(t *testing.T) *lockedLog {
	t.Helper()
	failures.flush()
	serverLog := &lockedLog{}
	previous := log.Writer()
	log.SetOutput(serverLog)
	t.Cleanup(func() {
		failures.flush()
		log.SetOutput(previous)
	})
	return serverLog
}

// requireLogged checks that the server log names each cause and holds no
// password.
func requireLogged(t *testing.T, serverLog *lockedLog, lines ...string) {
	t.Helper()
	logged := serverLog.String()
	for _, line := range lines {
		if !strings.Contains(logged, line) {
			t.Fatalf("server log lacks %q:\n%s", line, logged)
		}
	}
	for _, secret := range []string{"shared-password", "admin-password", "wrong-password"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("server log contains a password:\n%s", logged)
		}
	}
}

// failAttemptClearing gives the test client one earlier wrong password of
// each kind and makes clearing it fail, as a request whose deadline passes
// during the password hash does. A correct password then cannot finish its
// check.
func failAttemptClearing(t *testing.T, store *state.Store) {
	t.Helper()
	for _, kind := range []string{"general", "admin"} {
		noErr(t, store.RecordFailedAttempt(context.Background(), kind, "127.0.0.1", time.Now()))
	}
	refuseWrites(t, store, "refuse_clearing", "DELETE ON login_attempts")
}

// A password check that could not be completed says nothing about the
// password: the API answers 503 without a challenge and changes nothing.
func TestAPIPasswordCheckThatCouldNotFinishIsUnavailable(t *testing.T) {
	for _, failure := range []struct {
		name   string
		wrong  bool
		inject func(*testing.T, *state.Store)
	}{
		{"correct password, attempts not cleared", false, failAttemptClearing},
		{"wrong password, attempt not recorded", true, func(t *testing.T, store *state.Store) {
			refuseWrites(t, store, "refuse_recording", "INSERT ON login_attempts")
		}},
		{"stored password unreadable", false, func(t *testing.T, store *state.Store) {
			noErr(t, store.Exec(context.Background(), `UPDATE passwords SET encoded='not-a-password-hash'`))
		}},
	} {
		t.Run(failure.name, func(t *testing.T) {
			fixture := newAPIFixture(t, true)
			failure.inject(t, fixture.store)
			server := serve(t, fixture.app.Handler())
			serverLog := captureServerLog(t)
			general, admin := "shared-password", "admin-password"
			if failure.wrong {
				general, admin = "wrong-password", "wrong-password"
			}
			base := server.URL + "/api/v1/repositories/project"
			for _, response := range []*http.Response{
				apiRequest(t, http.MethodPost, base+"/pull-requests", map[string]any{
					"title": "Unverified", "source_branch": "feature", "target_branch": "main", "review": "skip",
				}, general, ""),
				adminAPIRequest(t, http.MethodPost, base+"/helper-credentials", map[string]any{"label": "unverified"}, admin),
			} {
				challenge := response.Header.Get("WWW-Authenticate")
				if status, code := checkStatus(t, response); status != http.StatusServiceUnavailable || code != "state_unavailable" || challenge != "" {
					t.Fatalf("%s status=%d code=%q challenge=%q", response.Request.URL.Path, status, code, challenge)
				}
			}
			listed, err := fixture.app.PullRequests.List(context.Background(), "project", pullrequest.ListInput{})
			noErr(t, err)
			pullRequests := listed.Items
			credentials, err := fixture.store.HelperCredentials(context.Background(), "project")
			noErr(t, err)
			if len(pullRequests) != 0 || len(credentials) != 0 {
				t.Fatalf("an unverified request changed state: pull requests=%d credentials=%d", len(pullRequests), len(credentials))
			}
			requireLogged(t, serverLog,
				"POST /api/v1/repositories/project/pull-requests: general password check could not be completed: ",
				"POST /api/v1/repositories/project/helper-credentials: admin password check could not be completed: ")
		})
	}
}

// requireUnavailableNotice checks a browser answer to a password check that
// could not be completed: 503, a page-level unavailable notice, and no
// password field marked as wrong.
func requireUnavailableNotice(t *testing.T, name string, result browserHTTPResult, wrongPassword webui.MessageCode) {
	t.Helper()
	notices := noticeRegion(t, result.body)
	if notices == "" {
		// A Settings group shows its notices inside the group.
		notices = groupNotices(result.body)
	}
	if result.status != http.StatusServiceUnavailable || !strings.Contains(notices, browserText(webui.MsgErrUnavailable)) ||
		strings.Contains(notices, browserText(wrongPassword)) || strings.Contains(result.body, `aria-invalid="true"`) {
		t.Fatalf("%s status=%d notices=%s", name, result.status, notices)
	}
}

// groupNotices returns the notices of the Settings groups on a page.
func groupNotices(body string) string {
	var notices []string
	for rest := body; ; {
		at := strings.Index(rest, "data-group-note")
		if at < 0 {
			return strings.Join(notices, "\n")
		}
		rest = rest[at:]
		end := strings.Index(rest, "</p>")
		if end < 0 {
			return strings.Join(notices, "\n")
		}
		notices, rest = append(notices, rest[:end]), rest[end:]
	}
}

func TestSignInThatCouldNotFinishIsUnavailable(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	browserGET(t, client, server.URL+"/admin/login")
	csrf := cookieValue(t, jar, server.URL, preauthCookie)
	serverLog := captureServerLog(t)

	// The password was right, but the session could not be saved.
	refuseWrites(t, fixture.store, "refuse_session", "INSERT ON sessions")
	result := browserForm(t, client, server.URL+"/admin/login", url.Values{"csrf": {csrf}, "admin_password": {"admin-password"}, "next": {"/"}}, server.URL)
	requireUnavailableNotice(t, "administrator sign-in", result, webui.MsgAdminFailed)
	noErr(t, fixture.store.Exec(context.Background(), `DROP TRIGGER refuse_session`))

	failAttemptClearing(t, fixture.store)
	endFailureWindows()
	result = browserForm(t, client, server.URL+"/login", url.Values{"csrf": {csrf}, "password": {"shared-password"}, "next": {"/"}}, server.URL)
	requireUnavailableNotice(t, "shared sign-in", result, webui.MsgLoginFailed)
	parsed, _ := url.Parse(server.URL)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == generalCookie || cookie.Name == adminCookie {
			t.Fatalf("a sign-in that could not finish set %s", cookie.Name)
		}
	}
	requireLogged(t, serverLog, "POST /admin/login: sign-in could not be completed: ", "POST /login: sign-in could not be completed: ")
}

// A rate limit is answered 429 on the sign-in and Settings forms too, so a
// browser form's 401 always means a wrong password.
func TestRateLimitedBrowserPasswordIsTooManyRequests(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	browserGET(t, client, server.URL+"/login")
	preauth := cookieValue(t, jar, server.URL, preauthCookie)
	for _, kind := range []string{"general", "admin"} {
		for range 4 {
			noErr(t, fixture.store.RecordFailedAttempt(context.Background(), kind, "127.0.0.1", time.Now()))
		}
	}
	requireLocked := func(name string, result browserHTTPResult, locked webui.MessageCode) {
		t.Helper()
		if result.status != http.StatusTooManyRequests || !strings.Contains(result.body, browserText(locked)) {
			t.Fatalf("%s status=%d", name, result.status)
		}
	}
	requireLocked("shared sign-in", browserForm(t, client, server.URL+"/login", url.Values{"csrf": {preauth}, "password": {"shared-password"}, "next": {"/"}}, server.URL), webui.MsgLoginLocked)
	requireLocked("administrator sign-in", browserForm(t, client, server.URL+"/admin/login", url.Values{"csrf": {preauth}, "admin_password": {"admin-password"}, "next": {"/"}}, server.URL), webui.MsgAdminLocked)

	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(context.Background(), "limited-session", "general", "limited-csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: generalCookie, Value: "limited-session", Path: "/"}})
	requireLocked("settings", browserForm(t, client, server.URL+"/settings", url.Values{
		"csrf": {"limited-csrf"}, "action": {webui.ActionChangeAdminPassword}, "admin_password": {"admin-password"}, "new_admin_password": {"admin-password-new"},
	}, server.URL), webui.MsgAdminLocked)
}

// Every administrator form that asks for the password again reports a check
// that could not be completed as unavailable, keeps what the form can show
// again, and changes nothing.
func TestAdminFormsReportAPasswordCheckThatCouldNotFinish(t *testing.T) {
	fixture := newAPIFixture(t, false)
	askEveryTime(t, fixture.app)
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "unavailable-admin")
	ctx := context.Background()
	// A saved policy makes the runner token form appear, so its refusal can
	// show the typed label again.
	_, err := fixture.store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, time.Now())
	noErr(t, err)
	failAttemptClearing(t, fixture.store)
	serverLog := captureServerLog(t)
	policy := validPolicyValues(csrf)
	policy.Set("queue_limit", "17")
	// kept is a submitted value the refused form shows again, or "" when the
	// form shows none.
	for _, form := range []struct {
		name, path string
		values     url.Values
		kept       string
		unchanged  func() bool
	}{
		{"settings", "/settings", url.Values{"action": {webui.ActionChangeAdminPassword}, "new_admin_password": {"admin-password-new"}}, "", func() bool {
			encoded, err := fixture.store.PasswordHash(ctx, "admin")
			return err == nil && auth.CheckPassword(encoded, "admin-password")
		}},
		{"helper credentials", baseHelperCredentialsURL("project"), url.Values{"action": {webui.ActionIssueHelperCredential}, "label": {"unverified"}}, `value="unverified"`, func() bool {
			credentials, err := fixture.store.HelperCredentials(ctx, "project")
			return err == nil && len(credentials) == 0
		}},
		{"check policy", configuredChecksURL("project"), policy, `value="17"`, func() bool {
			saved, _, err := fixture.store.CheckPolicy(ctx, "project")
			return err == nil && saved.QueueLimit == 4
		}},
		{"runner tokens", runnerTokensURL("project"), url.Values{"action": {webui.ActionIssueRunnerToken}, "label": {"kept runner label"}, "creation_id": {strings.Repeat("a", 32)}}, `value="kept runner label"`, func() bool {
			credentials, err := fixture.store.CheckRunnerCredentials(ctx, "project")
			return err == nil && len(credentials) == 0
		}},
		{"new import", "/repositories/new-import", url.Values{"name": {"kept-import"}, "url": {"https://example.invalid/team/kept.git"}, "mode": {"standalone"}}, "https://example.invalid/team/kept.git", func() bool {
			_, exists, err := fixture.store.Repository(ctx, "kept-import")
			return err == nil && !exists
		}},
		{"repository deletion", "/repositories/project/delete", url.Values{"mode": {webui.DeleteModeDeleteFiles}, "confirm_name": {"project"}}, `value="delete_files" required checked`, func() bool {
			_, exists, err := fixture.store.Repository(ctx, "project")
			return err == nil && exists
		}},
	} {
		form.values.Set("csrf", csrf)
		form.values.Set("admin_password", "admin-password")
		endFailureWindows()
		result := browserForm(t, client, server.URL+form.path, form.values, server.URL)
		requireUnavailableNotice(t, form.name, result, webui.MsgAdminFailed)
		if (form.kept != "" && !strings.Contains(result.body, form.kept)) || !form.unchanged() {
			t.Fatalf("%s lost the submitted %q or changed state", form.name, form.kept)
		}
		requireLogged(t, serverLog, "POST "+form.path+": administrator password check could not be completed: ")
	}
}

// A Git client whose password check could not be completed is told the
// server is unavailable, not asked for another password. A rate limit is
// answered 429 without a challenge.
func TestGitPasswordCheckThatCouldNotFinishIsUnavailable(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	failAttemptClearing(t, fixture.store)
	serverLog := captureServerLog(t)
	discover := func() (int, http.Header) {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/git/project.git/info/refs?service=git-upload-pack", nil)
		noErr(t, err)
		request.SetBasicAuth("owngit", "shared-password")
		response, err := http.DefaultClient.Do(request)
		noErr(t, err)
		response.Body.Close()
		return response.StatusCode, response.Header
	}
	if status, header := discover(); status != http.StatusServiceUnavailable || header.Get("WWW-Authenticate") != "" {
		t.Fatalf("unfinished check status=%d header=%v", status, header)
	}
	requireLogged(t, serverLog, "GET /git/project.git/info/refs: Git password check could not be completed: ")
	for range 3 {
		noErr(t, fixture.store.RecordFailedAttempt(context.Background(), "general", "127.0.0.1", time.Now()))
	}
	// Retry-After is what remains of the 15 minute pause, so Git does not
	// repeat the request before it ends.
	status, header := discover()
	retry, err := strconv.Atoi(header.Get("Retry-After"))
	if status != http.StatusTooManyRequests || header.Get("WWW-Authenticate") != "" || err != nil || retry < 890 || retry > 900 {
		t.Fatalf("rate-limited status=%d header=%v", status, header)
	}
}

// Git erases the password its credential helper stored when the server
// answers 401. During a lockout Git is told why it was refused, and the
// right password it stored stays.
func TestGitLockoutKeepsTheStoredPassword(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	for range 4 {
		noErr(t, fixture.store.RecordFailedAttempt(context.Background(), "general", "127.0.0.1", time.Now()))
	}
	home := t.TempDir()
	emptyConfig := filepath.Join(home, "gitconfig")
	noErr(t, os.WriteFile(emptyConfig, nil, 0o600))
	credentials := filepath.Join(home, "credentials")
	stored, err := url.Parse(server.URL)
	noErr(t, err)
	stored.User = url.UserPassword("owngit", "shared-password")
	noErr(t, os.WriteFile(credentials, []byte(stored.String()+"\n"), 0o600))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-c", "credential.helper=", "-c", "credential.helper=store --file="+filepath.ToSlash(credentials),
		"ls-remote", server.URL+"/git/project.git")
	command.Env = testfixture.GitEnvironment(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+emptyConfig, "HOME="+home))
	output, err := command.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(output), "429") {
		t.Fatalf("locked-out ls-remote err=%v output:\n%s", err, output)
	}
	if content, err := os.ReadFile(credentials); err != nil || string(content) != stored.String()+"\n" {
		t.Fatalf("the stored password did not survive the lockout: err=%v, file holds %d bytes", err, len(content))
	}
}

// Sign-out that the server could not record is not reported as done. The
// browser keeps its cookie so it can sign out again.
func TestSignOutThatCouldNotBeRecordedIsNotReportedAsDone(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(context.Background(), "signed-in-session", "general", "signed-in-csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: generalCookie, Value: "signed-in-session", Path: "/"}})

	refuseWrites(t, fixture.store, "refuse_signout", "DELETE ON sessions")
	serverLog := captureServerLog(t)
	result := browserForm(t, client, server.URL+"/logout", url.Values{"csrf": {"signed-in-csrf"}}, server.URL)
	_, live, err := fixture.store.Session(context.Background(), "signed-in-session", "general", time.Now())
	if result.status != http.StatusServiceUnavailable || !strings.Contains(result.body, browserText(webui.MsgLogoutFailed)) ||
		cookieValue(t, jar, server.URL, generalCookie) != "signed-in-session" || err != nil || !live {
		t.Fatalf("failed sign-out status=%d location=%q live=%v err=%v", result.status, result.header.Get("Location"), live, err)
	}
	requireLogged(t, serverLog, "POST /logout: sign-out could not be completed: ")
	if strings.Contains(serverLog.String(), "signed-in-session") {
		t.Fatal("the sign-out log names the session token")
	}

	noErr(t, fixture.store.Exec(context.Background(), `DROP TRIGGER refuse_signout`))
	result = browserForm(t, client, server.URL+"/logout", url.Values{"csrf": {"signed-in-csrf"}}, server.URL)
	_, live, err = fixture.store.Session(context.Background(), "signed-in-session", "general", time.Now())
	if result.status != http.StatusSeeOther || err != nil || live {
		t.Fatalf("retried sign-out status=%d live=%v err=%v", result.status, live, err)
	}
}

// A client that went away caused nothing an operator could fix, and a
// repository being prepared had its cause logged by preparation, so neither
// is logged again as an unavailable answer.
func TestClientThatLeftIsNotLoggedAsUnavailable(t *testing.T) {
	serverLog := captureServerLog(t)
	left, cancel := context.WithCancel(context.Background())
	cancel()
	logFailure(httptest.NewRequestWithContext(left, http.MethodPost, "/login", nil), "sign-in", context.Canceled)
	logFailure(httptest.NewRequest(http.MethodGet, "/repositories/project", nil), "repository read", fmt.Errorf("read: %w", repository.ErrRepositoryPreparing))
	if logged := serverLog.String(); logged != "" {
		t.Fatalf("a cause already accounted for was logged: %q", logged)
	}
}
