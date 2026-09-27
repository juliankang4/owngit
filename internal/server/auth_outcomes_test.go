package server

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// refuseWrites makes every statement matching event, such as
// "DELETE ON login_attempts", fail until the trigger is dropped.
func refuseWrites(t *testing.T, store *state.Store, trigger, event string) {
	t.Helper()
	noErr(t, store.Exec(context.Background(), `CREATE TRIGGER `+trigger+` BEFORE `+event+` BEGIN SELECT RAISE(ABORT, 'injected failure'); END`))
}

// captureServerLog sends the standard logger to a buffer for this test.
func captureServerLog(t *testing.T) *lockedLog {
	t.Helper()
	serverLog := &lockedLog{}
	previous := log.Writer()
	log.SetOutput(serverLog)
	t.Cleanup(func() { log.SetOutput(previous) })
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
		noErr(t, store.RecordFailedAttempt(context.Background(), kind, "127.0.0.1", time.Now(), 4, 10*time.Minute, 15*time.Minute))
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
			pullRequests, err := fixture.app.PullRequests.List(context.Background(), "project")
			noErr(t, err)
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
	if result.status != http.StatusServiceUnavailable || !strings.Contains(notices, browserText(webui.MsgErrUnavailable)) ||
		strings.Contains(notices, browserText(wrongPassword)) || strings.Contains(result.body, `aria-invalid="true"`) {
		t.Fatalf("%s status=%d notices=%s", name, result.status, notices)
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
			noErr(t, fixture.store.RecordFailedAttempt(context.Background(), kind, "127.0.0.1", time.Now(), 4, 10*time.Minute, 15*time.Minute))
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
		{"helper credentials", baseHelperCredentialsURL("project"), url.Values{"action": {webui.ActionIssueHelperCredential}, "label": {"unverified"}}, "", func() bool {
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
// still a refusal with a challenge.
func TestGitPasswordCheckThatCouldNotFinishIsUnavailable(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	failAttemptClearing(t, fixture.store)
	serverLog := captureServerLog(t)
	discover := func() (int, string) {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/git/project.git/info/refs?service=git-upload-pack", nil)
		noErr(t, err)
		request.SetBasicAuth("owngit", "shared-password")
		response, err := http.DefaultClient.Do(request)
		noErr(t, err)
		response.Body.Close()
		return response.StatusCode, response.Header.Get("WWW-Authenticate")
	}
	if status, challenge := discover(); status != http.StatusServiceUnavailable || challenge != "" {
		t.Fatalf("unfinished check status=%d challenge=%q", status, challenge)
	}
	requireLogged(t, serverLog, "GET /git/project.git/info/refs: Git password check could not be completed: ")
	for range 3 {
		noErr(t, fixture.store.RecordFailedAttempt(context.Background(), "general", "127.0.0.1", time.Now(), 4, 10*time.Minute, 15*time.Minute))
	}
	if status, challenge := discover(); status != http.StatusUnauthorized || challenge == "" {
		t.Fatalf("rate-limited status=%d challenge=%q", status, challenge)
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

// A client that went away caused nothing an operator could fix, so it is
// not logged as an unavailable answer.
func TestClientThatLeftIsNotLoggedAsUnavailable(t *testing.T) {
	serverLog := captureServerLog(t)
	logUnavailable(httptest.NewRequest(http.MethodPost, "/login", nil), "sign-in", context.Canceled)
	if logged := serverLog.String(); logged != "" {
		t.Fatalf("a client that left was logged: %q", logged)
	}
}
