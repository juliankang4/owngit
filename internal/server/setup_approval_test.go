package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/webui"
)

// approvalApp is an uninitialized app whose setup runs in the terminal, with
// browser approval open and a clock the test controls.
func approvalApp(t *testing.T) (*App, *httptest.Server, *time.Time) {
	t.Helper()
	app, _, _ := newTestApp(t)
	now := time.Now()
	app.Approvals = NewSetupApprovals()
	app.Approvals.Now = func() time.Time { return now }
	app.Approvals.Open()
	return app, serve(t, app.Handler()), &now
}

type setupBrowser struct {
	t      *testing.T
	client *http.Client
	jar    http.CookieJar
	server *httptest.Server
}

func newSetupBrowser(t *testing.T, server *httptest.Server) *setupBrowser {
	client, jar := newBrowserClient(t)
	return &setupBrowser{t: t, client: client, jar: jar, server: server}
}

func (b *setupBrowser) get(path string) (int, string, string) {
	b.t.Helper()
	response, err := b.client.Get(b.server.URL + path)
	noErr(b.t, err)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, response.Header.Get("Location"), string(body)
}

func (b *setupBrowser) post(path string, values url.Values) (int, string, string) {
	b.t.Helper()
	request, err := http.NewRequest(http.MethodPost, b.server.URL+path, strings.NewReader(values.Encode()))
	noErr(b.t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", b.server.URL)
	response, err := b.client.Do(request)
	noErr(b.t, err)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, response.Header.Get("Location"), string(body)
}

func (b *setupBrowser) cookie(name string) string {
	parsed, _ := url.Parse(b.server.URL)
	for _, cookie := range b.jar.Cookies(parsed) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

// ask opens /setup and asks the terminal for approval.
func (b *setupBrowser) ask() (int, string) {
	b.t.Helper()
	status, _, body := b.get("/setup")
	if status != http.StatusOK || !strings.Contains(body, `action="/setup/approval"`) {
		b.t.Fatalf("setup page status=%d does not offer approval:\n%s", status, body)
	}
	status, _, body = b.post("/setup/approval", url.Values{"csrf": {b.cookie(preauthCookie)}})
	return status, body
}

func TestApprovedBrowserGetsExactlyOneSetupSession(t *testing.T) {
	app, server, _ := approvalApp(t)
	browser := newSetupBrowser(t, server)
	if status, _ := browser.ask(); status != http.StatusSeeOther {
		t.Fatalf("request status=%d", status)
	}
	request, ok := app.Approvals.Pending()
	if !ok || len(request.Code) != 7 || !request.Loopback {
		t.Fatalf("pending=%+v ok=%v", request, ok)
	}
	for _, r := range strings.ReplaceAll(request.Code, "-", "") {
		if !strings.ContainsRune(approvalAlphabet, r) {
			t.Fatalf("code %q uses %q", request.Code, r)
		}
	}
	// Before approval the page only shows the code; no setup session.
	status, _, body := browser.get("/setup")
	if status != http.StatusOK || !strings.Contains(body, request.Code) || !strings.Contains(body, "data-approval-wait") {
		t.Fatalf("waiting page status=%d:\n%s", status, body)
	}
	if browser.cookie(setupCookie) != "" {
		t.Fatal("a setup session exists before approval")
	}
	// Another browser cannot use this approval.
	other := newSetupBrowser(t, server)
	if status, _ := other.ask(); status != http.StatusConflict {
		t.Fatalf("second browser while one is pending: status=%d, want 409", status)
	}
	noErr(t, app.Approvals.Decide(request.ID, true))
	if status, _, body := other.get("/setup"); status != http.StatusOK || strings.Contains(body, request.Code) || other.cookie(setupCookie) != "" {
		t.Fatalf("another browser got the approval: status=%d", status)
	}
	approvalValue := browser.cookie(approvalCookie)
	status, location, _ := browser.get("/setup")
	if status != http.StatusSeeOther || location != "/setup" || browser.cookie(setupCookie) == "" {
		t.Fatalf("approved browser status=%d location=%q", status, location)
	}
	status, _, body = browser.get("/setup")
	if status != http.StatusOK || !strings.Contains(body, `name="storage_path"`) {
		t.Fatalf("approved browser does not see the setup form: status=%d", status)
	}
	// The approval is used up: replaying its cookie in a fresh browser gives
	// nothing.
	replay := newSetupBrowser(t, server)
	parsed, _ := url.Parse(server.URL)
	replay.jar.SetCookies(parsed, []*http.Cookie{{Name: approvalCookie, Value: approvalValue}})
	if _, _, body := replay.get("/setup"); strings.Contains(body, `name="storage_path"`) || replay.cookie(setupCookie) != "" {
		t.Fatal("a replayed approval cookie was redeemed again")
	}
	if err := app.Approvals.Decide(request.ID, true); err != ErrApprovalGone {
		t.Fatalf("second decision err=%v", err)
	}
}

func TestRejectedBrowserCannotContinueOrRetryAtOnce(t *testing.T) {
	app, server, now := approvalApp(t)
	browser := newSetupBrowser(t, server)
	browser.ask()
	request, _ := app.Approvals.Pending()
	noErr(t, app.Approvals.Decide(request.ID, false))
	status, _, body := browser.get("/setup")
	if status != http.StatusForbidden || browser.cookie(setupCookie) != "" || !strings.Contains(body, "rejected this browser") {
		t.Fatalf("rejected browser status=%d:\n%s", status, body)
	}
	if status, _, body := browser.post("/setup/approval", url.Values{"csrf": {browser.cookie(preauthCookie)}}); status != http.StatusTooManyRequests || !strings.Contains(body, "rejected this browser") {
		t.Fatalf("immediate retry status=%d", status)
	}
	if _, ok := app.Approvals.Pending(); ok {
		t.Fatal("the rejected browser asked again at once")
	}
	// Another browser on the same computer is not held up by the rejection.
	if status, _ := newSetupBrowser(t, server).ask(); status != http.StatusSeeOther {
		t.Fatalf("another browser after a rejection: status=%d", status)
	}
	app.Approvals.Shut()
	app.Approvals.Open()
	*now = now.Add(approvalCooldown + time.Second)
	if status, _ := browser.ask(); status != http.StatusSeeOther {
		t.Fatalf("retry after the cooldown status=%d", status)
	}
}

func TestApprovalRequestsExpire(t *testing.T) {
	app, server, now := approvalApp(t)
	browser := newSetupBrowser(t, server)
	browser.ask()
	request, _ := app.Approvals.Pending()
	*now = now.Add(approvalLifetime + time.Second)
	if _, ok := app.Approvals.Pending(); ok {
		t.Fatal("an expired request is still pending")
	}
	if err := app.Approvals.Decide(request.ID, true); err != ErrApprovalGone {
		t.Fatalf("approving an expired request err=%v", err)
	}
	if status, _, _ := browser.get("/setup"); status != http.StatusOK || browser.cookie(setupCookie) != "" {
		t.Fatalf("expired request status=%d", status)
	}
	// An approval that is not redeemed in time expires too.
	browser = newSetupBrowser(t, server)
	browser.ask()
	request, _ = app.Approvals.Pending()
	noErr(t, app.Approvals.Decide(request.ID, true))
	*now = now.Add(approvalLifetime + time.Second)
	if status, _, _ := browser.get("/setup"); status == http.StatusSeeOther || browser.cookie(setupCookie) != "" {
		t.Fatalf("an expired approval was redeemed: status=%d", status)
	}
}

func TestApprovalRequestsAreRateLimitedPerAddress(t *testing.T) {
	approvals := NewSetupApprovals()
	approvals.Open()
	for i := 0; i < approvalLimit; i++ {
		if _, refusal := approvals.request("192.0.2.10", [32]byte{byte(i)}); refusal != nil {
			t.Fatalf("request %d refused: %+v", i, refusal)
		}
		approvals.Shut()
		approvals.Open()
	}
	if _, refusal := approvals.request("192.0.2.10", [32]byte{99}); refusal == nil || refusal.status != http.StatusTooManyRequests {
		t.Fatalf("request over the limit: %+v", refusal)
	}
	if _, refusal := approvals.request("192.0.2.11", [32]byte{100}); refusal != nil {
		t.Fatalf("another address was limited: %+v", refusal)
	}
}

func TestApprovalRequestNeedsCSRFAndOrigin(t *testing.T) {
	app, server, _ := approvalApp(t)
	browser := newSetupBrowser(t, server)
	browser.get("/setup")
	if status, _, _ := browser.post("/setup/approval", url.Values{"csrf": {"wrong"}}); status != http.StatusForbidden {
		t.Fatalf("wrong CSRF status=%d", status)
	}
	response := request(t, browser.client, http.MethodPost, server.URL+"/setup/approval", url.Values{"csrf": {browser.cookie(preauthCookie)}}, "")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing Origin status=%d", response.StatusCode)
	}
	if _, ok := app.Approvals.Pending(); ok {
		t.Fatal("a refused request is pending")
	}
}

func TestApprovalFromAnotherDeviceIsMarked(t *testing.T) {
	app, _, _ := approvalApp(t)
	recorder := httptest.NewRecorder()
	get := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/setup", nil)
	get.RemoteAddr = "192.0.2.10:50000"
	app.Handler().ServeHTTP(recorder, get)
	csrf := ""
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == preauthCookie {
			csrf = cookie.Value
		}
	}
	post := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/setup/approval", strings.NewReader(url.Values{"csrf": {csrf}}.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.Header.Set("Origin", "http://127.0.0.1")
	post.AddCookie(&http.Cookie{Name: preauthCookie, Value: csrf})
	post.RemoteAddr = "192.0.2.10:50000"
	recorder = httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, post)
	request, ok := app.Approvals.Pending()
	if recorder.Code != http.StatusSeeOther || !ok || request.Loopback || request.Address != "192.0.2.10" {
		t.Fatalf("status=%d request=%+v ok=%v", recorder.Code, request, ok)
	}
}

func TestApprovalIsRefusedWhenTheTerminalIsNotWaiting(t *testing.T) {
	app, server, _ := approvalApp(t)
	app.Approvals.Shut()
	browser := newSetupBrowser(t, server)
	if status, body := browser.ask(); status != http.StatusConflict || !strings.Contains(body, "not waiting for a browser") {
		t.Fatalf("status=%d", status)
	}
}

func TestSetupCompletionVoidsApprovals(t *testing.T) {
	app, server, _ := approvalApp(t)
	started := 0
	app.OnSetupComplete = func() { started++ }
	browser := newSetupBrowser(t, server)
	browser.ask()
	root := filepath.Join(t.TempDir(), "repositories")
	notices, err := app.CompleteSetup(context.Background(), SetupAnswers{
		StoragePath: root, AccessMode: "open", AdminPassword: "admin-password-one",
	}, false)
	if len(notices) != 0 || err != nil {
		t.Fatalf("notices=%v err=%v", notices, err)
	}
	select {
	case <-app.Approvals.Done():
	default:
		t.Fatal("approvals were not voided")
	}
	if _, ok := app.Approvals.Pending(); ok || started != 1 {
		t.Fatalf("pending request survived setup, or completion ran %d times", started)
	}
	if status, _, _ := browser.post("/setup/approval", url.Values{"csrf": {browser.cookie(preauthCookie)}}); status != http.StatusConflict {
		t.Fatalf("request after setup status=%d", status)
	}
}

// Switching to terminal setup ends the approved browser's setup session.
func TestEndBrowserSetupEndsApprovedSessions(t *testing.T) {
	app, server, _ := approvalApp(t)
	browser := newSetupBrowser(t, server)
	browser.ask()
	request, _ := app.Approvals.Pending()
	noErr(t, app.Approvals.Decide(request.ID, true))
	browser.get("/setup")
	if browser.cookie(setupCookie) == "" {
		t.Fatal("no setup session after approval")
	}
	noErr(t, app.EndBrowserSetup(context.Background()))
	if _, _, body := browser.get("/setup"); strings.Contains(body, `name="storage_path"`) {
		t.Fatal("the browser kept its setup session")
	}
}

func TestTerminalAndWebSetupReachTheSameState(t *testing.T) {
	answers := SetupAnswers{AccessMode: "password", AccessPassword: "shared-password-1", AdminPassword: "admin-password-1", InsecureAccepted: true}
	web, webStore, webRoot := newTestApp(t)
	noErr(t, webStore.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	server := serve(t, web.Handler())
	browser := newSetupBrowser(t, server)
	browser.get("/setup")
	browser.post("/setup/redeem", url.Values{"csrf": {browser.cookie(preauthCookie)}, "token": {"synthetic-owner-token"}})
	session, ok, err := webStore.Session(context.Background(), browser.cookie(setupCookie), "setup", time.Now())
	if err != nil || !ok {
		t.Fatalf("setup session ok=%v err=%v", ok, err)
	}
	status, _, _ := browser.post("/setup", url.Values{
		"csrf": {session.CSRF}, "storage_path": {webRoot}, "access_mode": {"password"},
		"access_password": {answers.AccessPassword}, "admin_password": {answers.AdminPassword}, "insecure_ack": {"1"},
	})
	if status != http.StatusSeeOther {
		t.Fatalf("web setup status=%d", status)
	}
	terminal, terminalStore, terminalRoot := newTestApp(t)
	answers.StoragePath = terminalRoot
	if notices, err := terminal.CompleteSetup(context.Background(), answers, true); len(notices) != 0 || err != nil {
		t.Fatalf("terminal setup notices=%v err=%v", notices, err)
	}
	for _, c := range []struct {
		app  *App
		root string
	}{{web, webRoot}, {terminal, terminalRoot}} {
		settings, err := c.app.Store.Settings(context.Background())
		noErr(t, err)
		canonical, _ := filepath.EvalSymlinks(c.root)
		if !settings.Initialized || settings.AccessMode != "password" || !settings.InsecureHTTPAccepted || settings.RepositoryRoot != canonical {
			t.Fatalf("settings=%+v", settings)
		}
		for kind, password := range map[string]string{"access": answers.AccessPassword, "admin": answers.AdminPassword} {
			hash, err := c.app.Store.PasswordHash(context.Background(), kind)
			if err != nil || !auth.CheckPassword(hash, password) {
				t.Fatalf("%s password not saved: %v", kind, err)
			}
		}
		if snapshot, _ := c.app.Store.BootstrapSnapshot(context.Background()); snapshot.Present {
			t.Fatal("a setup capability survived setup")
		}
		if c.app.Repositories.RepositoryRoot() != canonical {
			t.Fatal("the repository root was not applied")
		}
	}
	_ = terminalStore
}

func TestRepositoryFolderCheckCreatesNothing(t *testing.T) {
	app, store, _ := newTestApp(t)
	base := t.TempDir()
	file := filepath.Join(base, "notes.txt")
	noErr(t, os.WriteFile(file, []byte("x"), 0o600))
	locked := filepath.Join(base, "locked")
	noErr(t, os.Mkdir(locked, 0o500))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	missing := filepath.Join(base, "new", "repositories")
	for _, c := range []struct {
		path string
		want string
	}{
		{"relative/git", "setup.storage.invalid"},
		{file, "setup.storage.not_directory"},
		{filepath.Join(file, "below"), "setup.storage.not_directory"},
		{store.Dir(), "setup.storage.overlaps_state"},
		{filepath.Join(store.Dir(), "repositories"), "setup.storage.overlaps_state"},
		{filepath.Dir(store.Dir()), "setup.storage.overlaps_state"},
		{missing, ""},
		{base, ""},
	} {
		if got := app.CheckRepositoryFolder(c.path); string(got) != c.want {
			t.Errorf("%s: %q want %q", c.path, got, c.want)
		}
	}
	if os.Geteuid() > 0 { // not root, and not Windows (-1)
		if got := app.CheckRepositoryFolder(filepath.Join(locked, "git")); got != "setup.storage.denied" {
			t.Errorf("unwritable folder: %q", got)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "new")); !os.IsNotExist(err) {
		t.Fatal("the check created the folder")
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 2 {
		t.Fatalf("the check left files behind: %v", entries)
	}
}

// The waiting page asks this endpoint for its own request's state. Asking
// never redeems anything, and it reports only the asking browser's request.
func TestApprovalStatusReportsOnlyAndRedeemsNothing(t *testing.T) {
	app, server, _ := approvalApp(t)
	browser := newSetupBrowser(t, server)
	state := func(b *setupBrowser) string {
		t.Helper()
		status, _, body := b.get("/setup/approval")
		if status != http.StatusOK {
			t.Fatalf("status endpoint answered %d", status)
		}
		return strings.TrimSpace(body)
	}
	if got := state(browser); got != `{"state":"none"}` {
		t.Fatalf("before asking: %s", got)
	}
	browser.ask()
	if got := state(browser); got != `{"state":"pending"}` {
		t.Fatalf("pending: %s", got)
	}
	if got := state(newSetupBrowser(t, server)); got != `{"state":"none"}` {
		t.Fatalf("another browser sees %s", got)
	}
	request, _ := app.Approvals.Pending()
	noErr(t, app.Approvals.Decide(request.ID, true))
	for i := 0; i < 2; i++ {
		if got := state(browser); got != `{"state":"approved"}` || browser.cookie(setupCookie) != "" {
			t.Fatalf("approved: %s, setup cookie %q", got, browser.cookie(setupCookie))
		}
	}
	browser.get("/setup")
	if browser.cookie(setupCookie) == "" {
		t.Fatal("the approved browser was not given its setup session")
	}
	if _, err := app.CompleteSetup(context.Background(), SetupAnswers{
		StoragePath: filepath.Join(t.TempDir(), "repositories"), AccessMode: "open", AdminPassword: "admin-password-one",
	}, false); err != nil {
		t.Fatal(err)
	}
	if got := state(browser); got != `{"state":"done"}` {
		t.Fatalf("after setup: %s", got)
	}
}

// A terminal setup that listens only on this computer is not asked about
// plain HTTP. The Settings page then offers the acknowledgement exactly as
// it does for any installation that has not given it.
func TestLocalTerminalSetupIsAskedAboutPlainHTTPLater(t *testing.T) {
	app, _, root := newTestApp(t)
	if notices, err := app.CompleteSetup(context.Background(), SetupAnswers{
		StoragePath: root, AccessMode: "open", AdminPassword: "admin-password-one",
	}, false); len(notices) != 0 || err != nil {
		t.Fatalf("notices=%v err=%v", notices, err)
	}
	settings, err := app.Store.Settings(context.Background())
	noErr(t, err)
	if !settings.Initialized || settings.InsecureHTTPAccepted {
		t.Fatalf("settings=%+v", settings)
	}
	server := serve(t, app.Handler())
	browser := newSetupBrowser(t, server)
	_, _, page := browser.get("/settings")
	if !strings.Contains(page, `value="acknowledge_insecure"`) {
		t.Fatal("the Settings page does not offer the plain HTTP acknowledgement")
	}
	csrf := browser.cookie(generalCookie)
	if status, _, _ := browser.post("/settings", url.Values{
		"csrf": {csrf}, "action": {"acknowledge_insecure"}, "insecure_ack": {"1"}, "admin_password": {"admin-password-one"},
	}); status != http.StatusSeeOther {
		t.Fatalf("acknowledgement status=%d", status)
	}
	settings, err = app.Store.Settings(context.Background())
	noErr(t, err)
	if !settings.InsecureHTTPAccepted {
		t.Fatal("the acknowledgement was not saved")
	}
	if _, _, page := browser.get("/settings"); strings.Contains(page, `value="acknowledge_insecure"`) {
		t.Fatal("the Settings page still asks after the acknowledgement")
	}
}

// When the terminal cannot be used after all, setup continues with the
// setup file. Browsers then get the ordinary setup page, as in a start
// without a terminal, and no approval request is offered or accepted.
func TestAbandonedApprovalServesTheSetupFileFlow(t *testing.T) {
	app, server, _ := approvalApp(t)
	waiting := newSetupBrowser(t, server)
	if status, _ := waiting.ask(); status != http.StatusSeeOther {
		t.Fatalf("request status=%d", status)
	}
	app.Approvals.Abandon()
	if _, ok := app.Approvals.Pending(); ok {
		t.Fatal("a request is still pending")
	}
	browser := newSetupBrowser(t, server)
	for _, b := range []*setupBrowser{browser, waiting} {
		status, _, body := b.get("/setup")
		if status != http.StatusOK || !strings.Contains(body, "data-redeem-form") ||
			strings.Contains(body, "data-redeem-optional") || strings.Contains(body, `action="/setup/approval"`) {
			t.Fatalf("status=%d, not the setup file page:\n%s", status, body)
		}
	}
	if status, _, _ := browser.post("/setup/approval", url.Values{"csrf": {browser.cookie(preauthCookie)}}); status != http.StatusNotFound {
		t.Fatalf("approval request status=%d", status)
	}
	if _, _, body := waiting.get("/setup/approval"); strings.TrimSpace(body) != `{"state":"none"}` {
		t.Fatalf("status %s", body)
	}
}

// follow requests path and follows redirects like a browser, returning the
// final path and page.
func (b *setupBrowser) follow(status int, location, body string) (string, string) {
	b.t.Helper()
	path := ""
	for i := 0; status == http.StatusSeeOther || status == http.StatusFound; i++ {
		if i == 10 {
			b.t.Fatal("too many redirects")
		}
		path = location
		status, location, body = b.get(location)
	}
	if status != http.StatusOK {
		b.t.Fatalf("%s answered %d", path, status)
	}
	return path, body
}

// QA-053: after first setup, the "Setup finished" notice is shown once on
// the dashboard, also when shared access first asks for the password, and
// also after setup in the terminal. It comes from the setup itself through
// the notice cookie, never from an address alone.
func TestSetupFinishedNoticeIsShownOnceAfterSetup(t *testing.T) {
	finished := webui.Text(webui.LangEN, webui.MsgSetupCompleted)
	for _, c := range []struct {
		name     string
		terminal bool
		mode     string
	}{
		{"web, shared password", false, "password"},
		{"web, open", false, "open"},
		{"terminal, shared password", true, "password"},
		{"terminal, open", true, "open"},
	} {
		t.Run(c.name, func(t *testing.T) {
			app, store, root := newTestApp(t)
			server := serve(t, app.Handler())
			browser := newSetupBrowser(t, server)
			answers := url.Values{
				"storage_path": {root}, "access_mode": {c.mode}, "access_password": {"shared-password-1"},
				"admin_password": {"admin-password-1"}, "insecure_ack": {"1"},
			}
			var path, page string
			if c.terminal {
				if notices, err := app.CompleteSetup(context.Background(), SetupAnswers{
					StoragePath: root, AccessMode: c.mode, AccessPassword: "shared-password-1", AdminPassword: "admin-password-1",
				}, false); len(notices) != 0 || err != nil {
					t.Fatalf("notices=%v err=%v", notices, err)
				}
				path, page = browser.follow(browser.get("/"))
			} else {
				noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
				browser.get("/setup")
				browser.post("/setup/redeem", url.Values{"csrf": {browser.cookie(preauthCookie)}, "token": {"synthetic-owner-token"}})
				session, ok, err := store.Session(context.Background(), browser.cookie(setupCookie), "setup", time.Now())
				if err != nil || !ok {
					t.Fatalf("setup session ok=%v err=%v", ok, err)
				}
				answers.Set("csrf", session.CSRF)
				path, page = browser.follow(browser.post("/setup", answers))
			}
			if c.mode == "password" {
				if !strings.HasPrefix(path, "/login") || strings.Contains(page, finished) {
					t.Fatalf("expected the sign-in page without the notice at %s", path)
				}
				path, page = browser.follow(browser.post("/login", url.Values{
					"csrf": {browser.cookie(preauthCookie)}, "password": {"shared-password-1"}, "next": {"/"},
				}))
			}
			if shown := strings.Count(page, ">"+finished+"<"); shown != 1 {
				t.Fatalf("the notice is shown %d times at %s", shown, path)
			}
			if _, page := browser.follow(browser.get(path)); strings.Contains(page, finished) {
				t.Fatal("the notice is shown again on reload")
			}
			if _, page := browser.follow(browser.get("/")); strings.Contains(page, finished) {
				t.Fatal("the notice is shown again on the dashboard")
			}
		})
	}
}

// Only the dashboard takes the "Setup finished" notice. Another page opened
// first after setup keeps its address, and the dashboard shows the notice
// later.
func TestSetupFinishedNoticeKeepsAnotherFirstPage(t *testing.T) {
	finished := ">" + webui.Text(webui.LangEN, webui.MsgSetupCompleted) + "<"
	app, _, root := newTestApp(t)
	if notices, err := app.CompleteSetup(context.Background(), SetupAnswers{
		StoragePath: root, AccessMode: "open", AdminPassword: "admin-password-1",
	}, false); len(notices) != 0 || err != nil {
		t.Fatalf("notices=%v err=%v", notices, err)
	}
	browser := newSetupBrowser(t, serve(t, app.Handler()))
	status, location, page := browser.get("/repositories/new")
	if status != http.StatusOK || strings.Contains(page, finished) {
		t.Fatalf("new repository page: status=%d location=%q", status, location)
	}
	if _, page := browser.follow(browser.get("/")); strings.Count(page, finished) != 1 {
		t.Fatal("the dashboard does not show the notice once")
	}
}
