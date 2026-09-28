package server

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// askEveryTime chooses Every time: an administrator session then only opens
// the administrator pages, and each change asks for the password. Tests of
// what one change's password does use it.
func askEveryTime(t *testing.T, app *App) {
	t.Helper()
	noErr(t, app.Auth.SetAdminConfirmation(context.Background(), state.ConfirmEveryTime))
}

// confirmationClock is the time the server and its sessions see, moved by
// the test.
type confirmationClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *confirmationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *confirmationClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// confirmationBrowser is one browser: its own cookies, so its own
// administrator confirmation.
type confirmationBrowser struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	jar    http.CookieJar
}

func newConfirmationFixture(t *testing.T, protected bool, choice state.AdminConfirmation) (apiFixture, *httptest.Server, *confirmationClock) {
	t.Helper()
	fixture := newAPIFixture(t, protected)
	clock := &confirmationClock{now: time.Now()}
	fixture.app.Now, fixture.app.Auth.Now = clock.Now, clock.Now
	noErr(t, fixture.app.Auth.SetAdminConfirmation(context.Background(), choice))
	return fixture, serve(t, fixture.app.Handler()), clock
}

// openConfirmationBrowser opens the dashboard in a new browser, signing in
// with the shared password when access is protected.
func openConfirmationBrowser(t *testing.T, server *httptest.Server, protected bool) *confirmationBrowser {
	t.Helper()
	client, jar := newBrowserClient(t)
	browser := &confirmationBrowser{t: t, server: server, client: client, jar: jar}
	if protected {
		browserGET(t, client, server.URL+"/login")
		preauth := browser.cookie(preauthCookie)
		result := browserForm(t, client, server.URL+"/login", url.Values{"csrf": {preauth}, "password": {"shared-password"}, "next": {"/"}}, server.URL)
		if result.status != http.StatusSeeOther {
			t.Fatalf("shared sign-in status=%d", result.status)
		}
	}
	return browser
}

func (b *confirmationBrowser) cookie(name string) string {
	parsed, _ := url.Parse(b.server.URL)
	for _, cookie := range b.jar.Cookies(parsed) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func (b *confirmationBrowser) get(path string) browserHTTPResult {
	return browserGET(b.t, b.client, b.server.URL+path)
}

// csrf is the token the forms of the Settings page carry for this browser.
func (b *confirmationBrowser) csrf() string {
	b.t.Helper()
	page := b.get("/settings")
	if page.status != http.StatusOK {
		b.t.Fatalf("settings status=%d", page.status)
	}
	return formValue(b.t, page.body, "csrf")
}

func (b *confirmationBrowser) post(path string, values url.Values) browserHTTPResult {
	if values.Get("csrf") == "" {
		values.Set("csrf", b.csrf())
	}
	return browserForm(b.t, b.client, b.server.URL+path, values, b.server.URL)
}

// saveUpdateCheck saves the update check, with password unless it is "".
func (b *confirmationBrowser) saveUpdateCheck(password string) browserHTTPResult {
	values := url.Values{"action": {webui.ActionSetUpdateCheck}, "update_check": {"off"}}
	if password != "" {
		values.Set("admin_password", password)
	}
	return b.post("/settings", values)
}

// adminSignIn confirms this browser on the administrator sign-in page.
func (b *confirmationBrowser) adminSignIn() {
	b.t.Helper()
	b.get("/admin/login")
	result := browserForm(b.t, b.client, b.server.URL+"/admin/login", url.Values{"csrf": {b.cookie(preauthCookie)}, "admin_password": {"admin-password"}, "next": {"/"}}, b.server.URL)
	if result.status != http.StatusSeeOther || b.cookie(adminCookie) == "" {
		b.t.Fatalf("administrator sign-in status=%d", result.status)
	}
}

func requireSaved(t *testing.T, what string, result browserHTTPResult) {
	t.Helper()
	if result.status != http.StatusSeeOther {
		t.Fatalf("%s: status=%d, want a saved change", what, result.status)
	}
}

func requirePasswordAsked(t *testing.T, what string, result browserHTTPResult) {
	t.Helper()
	if result.status != http.StatusUnauthorized || !strings.Contains(result.body, enText(webui.MsgAdminEmpty)) {
		t.Fatalf("%s: status=%d, want the administrator password asked for", what, result.status)
	}
}

// The default remembers a typed password in the browser it was typed in
// for 30 minutes from the moment it was typed. Browsing does not extend
// it, and another browser has no part of it.
func TestAdminConfirmationIsRememberedInOneBrowserFromTheTypedPassword(t *testing.T) {
	fixture, server, clock := newConfirmationFixture(t, false, state.DefaultAdminConfirmation)
	first := openConfirmationBrowser(t, server, false)

	requirePasswordAsked(t, "a change before any password", first.saveUpdateCheck(""))
	wrong := first.saveUpdateCheck("not-the-password")
	if wrong.status != http.StatusUnauthorized || first.cookie(adminCookie) != "" {
		t.Fatalf("a wrong password: status=%d, confirmed=%v", wrong.status, first.cookie(adminCookie) != "")
	}
	requireSaved(t, "a change with the password", first.saveUpdateCheck("admin-password"))
	token := first.cookie(adminCookie)
	if token == "" {
		t.Fatal("the typed password did not start this browser's confirmation")
	}
	session, ok, err := fixture.store.Session(context.Background(), token, "admin", clock.Now())
	if err != nil || !ok || session.Expires.Sub(clock.Now()) > 30*time.Minute {
		t.Fatalf("confirmation ok=%v err=%v ends in %s", ok, err, session.Expires.Sub(clock.Now()))
	}
	requireSaved(t, "a remembered change", first.saveUpdateCheck(""))

	second := openConfirmationBrowser(t, server, false)
	requirePasswordAsked(t, "another browser", second.saveUpdateCheck(""))

	clock.Add(20 * time.Minute)
	for _, page := range []string{"/", "/settings", "/repositories/project", baseHelperCredentialsURL("project")} {
		if result := first.get(page); result.status != http.StatusOK {
			t.Fatalf("%s status=%d", page, result.status)
		}
	}
	clock.Add(11 * time.Minute)
	requirePasswordAsked(t, "a change after the window, however much the browser was used", first.saveUpdateCheck(""))
	if result := first.get(baseHelperCredentialsURL("project")); result.status != http.StatusSeeOther || !strings.HasPrefix(result.header.Get("Location"), "/admin/login") {
		t.Fatalf("an administrator page after the window: status=%d location=%q", result.status, result.header.Get("Location"))
	}
}

// Every administrator change of the dashboard goes through the same gate:
// a remembered browser makes each without the password, and a browser that
// is not confirmed is asked for it by each.
func TestEveryDashboardAdministratorChangeFollowsTheConfirmation(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.DefaultAdminConfirmation)
	ctx := context.Background()
	_, err := fixture.store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner,
		AllowedEvents: []string{"push"}, MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, time.Now())
	noErr(t, err)
	// Each change is sent with the token of the page that holds its form.
	changes := []struct {
		name, page, path string
		values           url.Values
		saved            int
	}{
		{"default branch", repositorySettingsURL("project"), repositorySettingsURL("project") + "/default-branch", url.Values{"branch": {"main"}}, http.StatusSeeOther},
		{"helper credential", baseHelperCredentialsURL("project"), baseHelperCredentialsURL("project"), url.Values{"action": {webui.ActionIssueHelperCredential}, "label": {"gate"}}, http.StatusOK},
		{"runner token", runnerTokensURL("project"), runnerTokensURL("project"), url.Values{"action": {webui.ActionIssueRunnerToken}, "label": {"gate"}, "creation_id": {strings.Repeat("a", 32)}}, http.StatusOK},
		{"check consent", configuredChecksURL("project"), configuredChecksURL("project"), url.Values{"action": {webui.ActionDisableChecks}}, http.StatusSeeOther},
		{"import schedule", "/repositories/project/import", "/repositories/project/import", url.Values{"action": {webui.ActionImportSchedule}, "enabled": {"0"}, "interval": {"1h"}}, 0},
		{"settings", "/settings", "/settings", url.Values{"action": {webui.ActionSetUpdateCheck}, "update_check": {"on"}}, http.StatusSeeOther},
		{"deletion", repositoryDeleteURL("project"), repositoryDeleteURL("project"), url.Values{"mode": {webui.DeleteModeKeepFiles}, "confirm_name": {"project"}}, http.StatusSeeOther},
	}
	send := func(browser *confirmationBrowser, page, path string, values url.Values) browserHTTPResult {
		form := url.Values{}
		for key, value := range values {
			form[key] = value
		}
		form.Set("csrf", formValue(t, browser.get(page).body, "csrf"))
		return browser.post(path, form)
	}

	asked := openConfirmationBrowser(t, server, false)
	asked.adminSignIn()
	noErr(t, fixture.app.Auth.SetAdminConfirmation(ctx, state.ConfirmEveryTime))
	for _, change := range changes {
		if result := send(asked, change.page, change.path, change.values); result.status != http.StatusUnauthorized {
			t.Errorf("%s under Every time without the password: status=%d", change.name, result.status)
		}
	}

	noErr(t, fixture.app.Auth.SetAdminConfirmation(ctx, state.DefaultAdminConfirmation))
	remembered := openConfirmationBrowser(t, server, false)
	remembered.adminSignIn()
	for _, change := range changes {
		result := send(remembered, change.page, change.path, change.values)
		// An import schedule of a repository that is not imported is
		// refused by the import service, after the gate.
		if change.saved == 0 {
			if result.status == http.StatusUnauthorized {
				t.Errorf("%s asked a remembered browser for the password", change.name)
			}
			continue
		}
		if result.status != change.saved {
			t.Errorf("%s by a remembered browser: status=%d, want %d", change.name, result.status, change.saved)
		}
	}
	if _, exists, err := fixture.store.Repository(ctx, "project"); err != nil || exists {
		t.Fatalf("the remembered deletion left the repository: exists=%v err=%v", exists, err)
	}
}

// Under Every time the administrator sign-in only opens the administrator
// pages; each change asks, and a typed password is not remembered.
func TestEveryTimeAsksForEachChange(t *testing.T) {
	_, server, _ := newConfirmationFixture(t, false, state.ConfirmEveryTime)
	browser := openConfirmationBrowser(t, server, false)
	browser.adminSignIn()
	token := browser.cookie(adminCookie)
	if result := browser.get(baseHelperCredentialsURL("project")); result.status != http.StatusOK || !strings.Contains(result.body, `name="admin_password"`) {
		t.Fatalf("administrator page status=%d, or its forms do not ask for the password", result.status)
	}
	requirePasswordAsked(t, "a change after the sign-in", browser.saveUpdateCheck(""))
	requireSaved(t, "a change with the password", browser.saveUpdateCheck("admin-password"))
	if browser.cookie(adminCookie) != token {
		t.Fatal("a typed password started a remembered confirmation under Every time")
	}
	requirePasswordAsked(t, "the next change", browser.saveUpdateCheck(""))
}

// A shorter choice shortens the confirmations browsers hold already, this
// one's included; a longer one extends none.
func TestAShorterChoiceShortensHeldConfirmations(t *testing.T) {
	fixture, server, clock := newConfirmationFixture(t, false, state.Confirm30Days)
	ctx := context.Background()
	saver, other := openConfirmationBrowser(t, server, false), openConfirmationBrowser(t, server, false)
	saver.adminSignIn()
	other.adminSignIn()
	expires := func(browser *confirmationBrowser) time.Duration {
		session, ok, err := fixture.store.Session(ctx, browser.cookie(adminCookie), "admin", clock.Now())
		if err != nil || !ok {
			return 0
		}
		return session.Expires.Sub(clock.Now())
	}
	if expires(other) < 29*24*time.Hour {
		t.Fatalf("a 30 day confirmation ends in %s", expires(other))
	}
	requireSaved(t, "a shorter choice", saver.post("/settings/access", url.Values{"action": {webui.ActionSaveConfirmation}, "admin_confirmation": {"1h"}}))
	for name, browser := range map[string]*confirmationBrowser{"saver": saver, "other": other} {
		if left := expires(browser); left <= 0 || left > time.Hour {
			t.Fatalf("%s's confirmation ends in %s after choosing 1 hour", name, left)
		}
	}
	requireSaved(t, "a longer choice", saver.post("/settings/access", url.Values{"action": {webui.ActionSaveConfirmation}, "admin_confirmation": {"7d"}}))
	if left := expires(other); left > time.Hour {
		t.Fatalf("a longer choice extended a held confirmation to %s", left)
	}
	requireSaved(t, "Every time", saver.post("/settings/access", url.Values{"action": {webui.ActionSaveConfirmation}, "admin_confirmation": {"every"}}))
	requirePasswordAsked(t, "a held confirmation under Every time", other.saveUpdateCheck(""))
}

// End, signing out, and changing the administrator password end a
// remembered confirmation. Changing the password always asks for the
// current one.
func TestRememberedConfirmationEnds(t *testing.T) {
	fixture, server, clock := newConfirmationFixture(t, true, state.Confirm8Hours)
	ctx := context.Background()
	held := func(browser *confirmationBrowser) bool {
		_, ok, err := fixture.store.Session(ctx, browser.cookie(adminCookie), "admin", clock.Now())
		noErr(t, err)
		return ok
	}

	ended := openConfirmationBrowser(t, server, true)
	ended.adminSignIn()
	token := ended.cookie(adminCookie)
	if result := ended.post("/admin/logout", url.Values{}); result.status != http.StatusSeeOther {
		t.Fatalf("End status=%d", result.status)
	}
	if _, ok, _ := fixture.store.Session(ctx, token, "admin", clock.Now()); ok {
		t.Fatal("End left the confirmation")
	}
	requirePasswordAsked(t, "a change after End", ended.saveUpdateCheck(""))

	signedOut := openConfirmationBrowser(t, server, true)
	signedOut.adminSignIn()
	token = signedOut.cookie(adminCookie)
	if result := signedOut.post("/logout", url.Values{}); result.status != http.StatusSeeOther {
		t.Fatalf("sign-out status=%d", result.status)
	}
	if _, ok, _ := fixture.store.Session(ctx, token, "admin", clock.Now()); ok || signedOut.cookie(adminCookie) != "" {
		t.Fatal("signing out left the administrator confirmation")
	}

	changer, bystander := openConfirmationBrowser(t, server, true), openConfirmationBrowser(t, server, true)
	changer.adminSignIn()
	bystander.adminSignIn()
	change := url.Values{"action": {webui.ActionChangeAdminPassword}, "new_admin_password": {"admin-password-new"}}
	if result := changer.post("/settings/access", change); result.status != http.StatusUnauthorized {
		t.Fatalf("a remembered browser changed the administrator password without the current one: status=%d", result.status)
	}
	change.Set("admin_password", "admin-password")
	change.Del("csrf")
	requireSaved(t, "changing the administrator password", changer.post("/settings/access", change))
	if held(bystander) {
		t.Fatal("changing the administrator password left another browser confirmed")
	}
}

// A shared password equal to the administrator password is refused also
// when the save typed no administrator password.
func TestSharedPasswordEqualToTheAdminPasswordIsRefusedWithoutATypedPassword(t *testing.T) {
	_, server, _ := newConfirmationFixture(t, true, state.DefaultAdminConfirmation)
	browser := openConfirmationBrowser(t, server, true)
	browser.adminSignIn()
	result := browser.post("/settings/access", url.Values{"action": {webui.ActionSaveAccess}, "access_mode": {"password"}, "access_password": {"admin-password"}})
	if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, enText(webui.MsgSetupGenSameAsAdmin)) {
		t.Fatalf("status=%d, want the equal password refused", result.status)
	}
	// The new shared password signs this browser out of shared access, and
	// its administrator confirmation keeps Settings open.
	changed := browser.post("/settings/access", url.Values{"action": {webui.ActionSaveAccess}, "access_mode": {"password"}, "access_password": {"shared-password-2"}})
	if changed.status != http.StatusSeeOther || changed.header.Get("Location") != "/settings/access?notice=access_changed#grp-access" {
		t.Fatalf("a different shared password: status=%d location=%q", changed.status, changed.header.Get("Location"))
	}
}

// A remembered browser saves a Settings group through the script, which
// keeps the other groups' drafts, because the group has no password field.
func TestRememberedBrowserSavesAGroupWithoutAPasswordField(t *testing.T) {
	_, server, _ := newConfirmationFixture(t, false, state.DefaultAdminConfirmation)
	browser := openConfirmationBrowser(t, server, false)
	browser.adminSignIn()
	page := browser.get("/settings")
	start := strings.Index(page.body, `data-group="update"`)
	end := strings.Index(page.body[start:], "</form>")
	if start < 0 || end < 0 || strings.Contains(page.body[start:start+end], `type="password"`) {
		t.Fatal("the update group of a remembered browser still has a password field")
	}
	request, err := http.NewRequest(http.MethodPost, browser.server.URL+"/settings", strings.NewReader(url.Values{
		"csrf": {formValue(t, page.body, "csrf")}, "action": {webui.ActionSetUpdateCheck}, "update_check": {"off"},
	}.Encode()))
	noErr(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", browser.server.URL)
	request.Header.Set(settingsGroupHeader, webui.GroupUpdate)
	result := browserRequest(t, browser.client, request)
	if result.status != http.StatusOK || !strings.HasPrefix(result.header.Get("Content-Type"), "application/json") {
		t.Fatalf("script save status=%d type=%q", result.status, result.header.Get("Content-Type"))
	}
}

// Do not ask applies to every browser that may use the dashboard, and only
// to the dashboard. Turning it on asks for the password one last time and
// for the acknowledgement of the warning; while it is on every page says so.
func TestDoNotAskOpensTheDashboardButNotTheAPI(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.DefaultAdminConfirmation)
	ctx := context.Background()
	owner := openConfirmationBrowser(t, server, false)
	owner.adminSignIn()
	turnOff := url.Values{"action": {webui.ActionSaveConfirmation}, "admin_confirmation": {"never"}}
	if result := owner.post("/settings/access", turnOff); result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, enText(webui.MsgConfirmAckNeeded)) {
		t.Fatalf("Do not ask without the acknowledgement: status=%d", result.status)
	}
	turnOff.Set("no_ask_ack", "1")
	turnOff.Del("csrf")
	requirePasswordAsked(t, "Do not ask in a remembered browser", owner.post("/settings/access", turnOff))
	if choice, _, err := fixture.store.AdminConfirmation(ctx); err != nil || choice != state.DefaultAdminConfirmation {
		t.Fatalf("a refused save changed the choice to %q (%v)", choice, err)
	}
	turnOff.Set("admin_password", "admin-password")
	turnOff.Del("csrf")
	requireSaved(t, "Do not ask", owner.post("/settings/access", turnOff))

	visitor := openConfirmationBrowser(t, server, false)
	if page := visitor.get("/"); !strings.Contains(page.body, "data-admin-check-off") {
		t.Fatal("the dashboard does not say the administrator password check is off")
	}
	helper := visitor.get(baseHelperCredentialsURL("project"))
	if helper.status != http.StatusOK || strings.Contains(helper.body, `name="admin_password"`) {
		t.Fatalf("an administrator page under Do not ask: status=%d", helper.status)
	}
	issued := visitor.post(baseHelperCredentialsURL("project"), url.Values{"csrf": {formValue(t, helper.body, "csrf")}, "action": {webui.ActionIssueHelperCredential}, "label": {"no ask"}})
	if issued.status != http.StatusOK {
		t.Fatalf("issuing a credential under Do not ask: status=%d", issued.status)
	}
	requireSaved(t, "a Settings change under Do not ask", visitor.saveUpdateCheck(""))

	// The API keeps asking. The browser's cookies are not a credential.
	api := httptest.NewRequest(http.MethodPost, server.URL+"/api/v1/repositories/project/helper-credentials", strings.NewReader(`{"label":"api"}`))
	api.RequestURI = ""
	api.Header.Set("Content-Type", "application/json")
	api.Header.Set("Origin", server.URL)
	response, err := visitor.client.Do(api)
	noErr(t, err)
	status, code := checkStatus(t, response)
	if status != http.StatusUnauthorized || code != "admin_authentication_required" {
		t.Fatalf("the API under Do not ask: status=%d code=%q", status, code)
	}

	// A protected dashboard still asks for the shared password first, and
	// says nothing about the check before it.
	noErr(t, fixture.store.SetAccessPassword(ctx, fixturePasswordHash(t, "shared-password")))
	jar, err := cookiejar.New(nil)
	noErr(t, err)
	stranger := &confirmationBrowser{t: t, server: server, client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, jar: jar}
	if result := stranger.get(baseHelperCredentialsURL("project")); result.status != http.StatusSeeOther || !strings.HasPrefix(result.header.Get("Location"), "/login") {
		t.Fatalf("an administrator page for a stranger: status=%d location=%q", result.status, result.header.Get("Location"))
	}
	if page := stranger.get("/login"); strings.Contains(page.body, "data-admin-check-off") {
		t.Fatal("the sign-in page tells a stranger that the check is off")
	}
}

// A saved choice this build does not know, as after going back to an older
// release, acts as Every time until the owner chooses again: the dashboard
// keeps working, each change asks for the password, and saving a choice
// with the password replaces the value, Every time included.
func TestAnUnknownSavedChoiceActsAsEveryTime(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, false, state.Confirm8Hours)
	ctx := context.Background()
	browser := openConfirmationBrowser(t, server, false)
	browser.adminSignIn()
	noErr(t, fixture.store.Exec(ctx, `UPDATE metadata SET value='2h' WHERE key='admin_confirmation'`))

	for _, page := range []string{"/", "/settings", "/repositories/project", baseHelperCredentialsURL("project")} {
		if result := browser.get(page); result.status != http.StatusOK {
			t.Fatalf("%s status=%d", page, result.status)
		}
	}
	if result := openConfirmationBrowser(t, server, false).get("/repositories/project"); result.status != http.StatusOK {
		t.Fatalf("a repository page for another browser: status=%d", result.status)
	}
	access := browser.get("/settings/access")
	if access.status != http.StatusOK || !strings.Contains(access.body, enText(webui.MsgConfirmUnknown)) ||
		!strings.Contains(access.body, `name="admin_confirmation" data-saved=""`) {
		t.Fatalf("Access status=%d, or it does not say the saved choice was not recognized", access.status)
	}
	requirePasswordAsked(t, "a change without the password", browser.saveUpdateCheck(""))
	requireSaved(t, "a change with the password", browser.saveUpdateCheck("admin-password"))

	choose := url.Values{"action": {webui.ActionSaveConfirmation}, "admin_confirmation": {"every"}}
	requirePasswordAsked(t, "choosing again without the password", browser.post("/settings/access", choose))
	choose.Set("admin_password", "admin-password")
	choose.Del("csrf")
	requireSaved(t, "choosing Every time", browser.post("/settings/access", choose))
	if choice, known, err := fixture.store.AdminConfirmation(ctx); err != nil || !known || choice != state.ConfirmEveryTime {
		t.Fatalf("saved choice = %q, known=%v, err=%v", choice, known, err)
	}
	if page := browser.get("/settings/access"); strings.Contains(page.body, enText(webui.MsgConfirmUnknown)) {
		t.Fatal("Access still says the saved choice was not recognized")
	}
}
