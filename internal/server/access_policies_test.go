package server

import (
	"context"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// With the typed name turned off, deleting still asks what happens to the
// files and for the administrator password, and needs no name.
func TestDeletionWithoutTheTypedName(t *testing.T) {
	fixture := newAPIFixture(t, false)
	askEveryTime(t, fixture.app)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	settings := browserForm(t, client, server.URL+"/settings/repositories", url.Values{
		"csrf": {adminTestCSRF}, "action": {webui.ActionSaveDeleteName}, "delete_requires_name": {"off"}, "admin_password": {"admin-password"},
	}, server.URL)
	if settings.status != http.StatusSeeOther || !strings.Contains(settings.header.Get("Location"), "notice=delete_name_off") {
		t.Fatalf("saving the choice status=%d location=%q", settings.status, settings.header.Get("Location"))
	}
	if saved := browserGET(t, client, server.URL+settings.header.Get("Location")); !strings.Contains(saved.body, `name="delete_requires_name" data-saved="off"`) || !strings.Contains(saved.body, enText(webui.MsgDeleteNameSavedOff)) {
		t.Fatalf("Repositories tab after saving:\n%s", saved.body)
	}
	target := server.URL + "/repositories/project/delete"
	page := browserGET(t, client, target)
	if page.status != http.StatusOK || strings.Contains(page.body, `name="confirm_name"`) || !strings.Contains(page.body, `value="delete_files"`) || !strings.Contains(page.body, `name="admin_password"`) {
		t.Fatalf("delete page without the name status=%d:\n%s", page.status, page.body)
	}
	for _, refused := range []struct {
		values url.Values
		status int
	}{
		{url.Values{"csrf": {adminTestCSRF}, "admin_password": {"admin-password"}}, http.StatusUnprocessableEntity},
		{url.Values{"csrf": {adminTestCSRF}, "mode": {"keep_files"}, "admin_password": {"not-it"}}, http.StatusUnauthorized},
	} {
		if result := browserForm(t, client, target, refused.values, server.URL); result.status != refused.status {
			t.Fatalf("refused deletion status=%d want %d", result.status, refused.status)
		}
		assertRepositoryIntact(t, fixture, "refused deletion without the name")
	}
	result := browserForm(t, client, target, url.Values{"csrf": {adminTestCSRF}, "mode": {"keep_files"}, "admin_password": {"admin-password"}}, server.URL)
	if result.status != http.StatusSeeOther {
		t.Fatalf("deletion without the name status=%d:\n%s", result.status, result.body)
	}
	if _, exists, err := fixture.store.Repository(t.Context(), "project"); err != nil || exists {
		t.Fatalf("repository remains: exists=%v err=%v", exists, err)
	}
}

// A saved choice that cannot be read stops deletions and says how to set
// it again; it never falls back to either choice silently.
func TestUnreadableDeleteNameChoiceRefusesDeletion(t *testing.T) {
	fixture := newAPIFixture(t, false)
	noErr(t, fixture.store.Exec(context.Background(), `INSERT INTO metadata(key,value) VALUES('delete_requires_name','maybe')`))
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	target := server.URL + "/repositories/project/delete"
	if page := browserGET(t, client, target); page.status != http.StatusOK || !strings.Contains(page.body, enText(webui.MsgDeleteNameUnreadableDelete)) {
		t.Fatalf("delete page status=%d:\n%s", page.status, page.body)
	}
	result := browserForm(t, client, target, url.Values{"csrf": {adminTestCSRF}, "mode": {"keep_files"}, "confirm_name": {"project"}, "admin_password": {"admin-password"}}, server.URL)
	if result.status != http.StatusConflict || !strings.Contains(result.body, enText(webui.MsgDeleteNameUnreadableDelete)) {
		t.Fatalf("deletion status=%d", result.status)
	}
	assertRepositoryIntact(t, fixture, "deletion with an unreadable choice")
	if tab := browserGET(t, client, server.URL+"/settings/repositories"); !strings.Contains(tab.body, `name="delete_requires_name" data-saved=""`) {
		t.Fatalf("Repositories tab does not offer to set it again:\n%s", tab.body)
	}
	requireUnreadable(t, adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "admin-password"), "delete_requires_name", "delete_requires_name")
	if status, _, settings := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"delete_requires_name": "on"}); status != http.StatusOK || settings["delete_requires_name"] != "on" {
		t.Fatalf("PATCH status=%d settings=%v", status, settings)
	}
}

// The owner API reads and changes the login limits field by field, and
// refuses a value out of bounds without saving anything.
func TestSettingsAPIChangesTheLoginLimits(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	limits := func(settings map[string]any) map[string]any {
		value, _ := settings["login_limits"].(map[string]any)
		return value
	}
	status, _, settings := settingsAPI(t, server.URL, http.MethodGet, nil)
	if got := limits(settings); status != http.StatusOK || got["attempts"] != 4.0 || got["window_seconds"] != 600.0 || got["pause_seconds"] != 900.0 {
		t.Fatalf("GET status=%d settings=%v", status, settings)
	}
	status, _, settings = settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"login_limits": map[string]any{"attempts": 10}})
	if got := limits(settings); status != http.StatusOK || got["attempts"] != 10.0 || got["window_seconds"] != 600.0 || got["pause_seconds"] != 900.0 {
		t.Fatalf("PATCH attempts status=%d settings=%v", status, settings)
	}
	for _, refused := range []map[string]any{
		{"attempts": 0}, {"attempts": 101}, {"window_seconds": 59}, {"pause_seconds": 86401},
		{"pause_seconds": int64(math.MaxInt64)}, {"window_seconds": int64(math.MinInt64)}, {},
	} {
		if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"login_limits": refused}); status != http.StatusBadRequest || code != "invalid_settings" {
			t.Fatalf("PATCH %v status=%d code=%s", refused, status, code)
		}
	}
	saved, err := fixture.store.LoginLimits(context.Background())
	if err != nil || saved != (state.LoginLimits{Attempts: 10, Window: 10 * time.Minute, Pause: 15 * time.Minute}) {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
}

// Wrong passwords are counted with the limits saved when they arrive. A
// pause keeps the end it had when it started, and Retry-After says what
// remains of it.
func TestLoginLimitsApplyToNewFailuresAndRetryAfterIsTheRemainingPause(t *testing.T) {
	fixture, server, clock := newConfirmationFixture(t, true, state.ConfirmEveryTime)
	ctx := context.Background()
	two := state.LoginLimits{Attempts: 2, Window: 10 * time.Minute, Pause: 5 * time.Minute}
	noErr(t, fixture.store.SavePolicies(ctx, state.PolicyChange{LoginLimits: &two}))
	git := func(password string) (int, string) {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/git/project.git/info/refs?service=git-upload-pack", nil)
		noErr(t, err)
		request.SetBasicAuth("owngit", password)
		response, err := http.DefaultClient.Do(request)
		noErr(t, err)
		response.Body.Close()
		return response.StatusCode, response.Header.Get("Retry-After")
	}
	for range 2 {
		if status, _ := git("wrong-password"); status != http.StatusUnauthorized {
			t.Fatalf("wrong password status=%d", status)
		}
	}
	if status, retry := git("shared-password"); status != http.StatusTooManyRequests || retry != "300" {
		t.Fatalf("paused status=%d Retry-After=%q", status, retry)
	}
	// A longer pause saved now leaves this one's end as it is.
	long := state.LoginLimits{Attempts: 2, Window: 10 * time.Minute, Pause: time.Hour}
	noErr(t, fixture.store.SavePolicies(ctx, state.PolicyChange{LoginLimits: &long}))
	clock.Add(4 * time.Minute)
	if status, retry := git("shared-password"); status != http.StatusTooManyRequests || retry != "60" {
		t.Fatalf("after 4 minutes status=%d Retry-After=%q", status, retry)
	}
	response := apiRequest(t, http.MethodGet, server.URL+"/api/v1/repositories", nil, "shared-password", "")
	if retry, err := strconv.Atoi(response.Header.Get("Retry-After")); response.StatusCode != http.StatusTooManyRequests || err != nil || retry != 60 {
		t.Fatalf("API while paused status=%d Retry-After=%q", response.StatusCode, response.Header.Get("Retry-After"))
	}
	response.Body.Close()
	clock.Add(time.Minute)
	if status, _ := git("shared-password"); status != http.StatusOK {
		t.Fatalf("after the pause status=%d", status)
	}
	// New failures pause for the hour saved since. The administrator
	// password is counted apart.
	for range 2 {
		git("wrong-password")
	}
	if status, retry := git("shared-password"); status != http.StatusTooManyRequests || retry != "3600" {
		t.Fatalf("new pause status=%d Retry-After=%q", status, retry)
	}
	if status, _, _ := settingsAPI(t, server.URL, http.MethodGet, nil); status != http.StatusOK {
		t.Fatalf("administrator API during a shared password pause status=%d", status)
	}
}

// Wrong administrator passwords from enough different addresses pause every
// administrator check with the usual 429 and Retry-After, and name what the
// owner can do; reset-admin ends the pause.
func TestServerWideAdministratorPauseAnswersTooManyRequests(t *testing.T) {
	fixture, server, clock := newConfirmationFixture(t, true, state.ConfirmEveryTime)
	ctx := context.Background()
	one := state.LoginLimits{Attempts: 1, Window: 10 * time.Minute, Pause: 5 * time.Minute}
	noErr(t, fixture.store.SavePolicies(ctx, state.PolicyChange{LoginLimits: &one}))
	for client := range state.ServerWideFailureFactor {
		noErr(t, fixture.store.RecordFailedAttempt(ctx, "admin", fmt.Sprintf("192.0.2.%d", client+1), clock.Now()))
	}
	response := adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "admin-password")
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests || response.Header.Get("Retry-After") != "300" || !strings.Contains(string(body), "reset-admin") {
		t.Fatalf("paused administrator API status=%d Retry-After=%q body=%s", response.StatusCode, response.Header.Get("Retry-After"), body)
	}
	encoded, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, fixture.store.SetAdminPassword(ctx, encoded))
	if status, _, _ := settingsAPI(t, server.URL, http.MethodGet, nil); status != http.StatusOK {
		t.Fatalf("administrator API after reset status=%d", status)
	}
}

// Login limits that cannot be read never count a wrong password with a
// default: the wrong password is refused with the setting named, nothing
// is counted, and the right password still passes so that an
// administrator can set them again.
func TestUnreadableLoginLimitsStillLetTheRightPasswordIn(t *testing.T) {
	fixture := newAPIFixture(t, true)
	noErr(t, fixture.store.Exec(context.Background(), `INSERT INTO metadata(key,value) VALUES('login_limits','{"attempts":0}')`))
	server := serve(t, fixture.app.Handler())
	for range 6 {
		response := adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "not-the-password")
		requireUnreadable(t, response, "login_limits", "login_limits")
	}
	// Six wrong passwords counted under any limit would have paused the
	// address; the right one still reaches the API.
	requireUnreadable(t, adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "admin-password"), "login_limits", "login_limits")
	status, _, settings := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"login_limits": map[string]any{"attempts": 5, "window_seconds": 600, "pause_seconds": 900}})
	if status != http.StatusOK || settings["login_limits"] == nil {
		t.Fatalf("PATCH status=%d settings=%v", status, settings)
	}
	// Stored JSON of the wrong shape is as unreadable as a wrong value.
	for _, stored := range []string{`null`, `{}]`, `{"attempts":null}`} {
		noErr(t, fixture.store.Exec(context.Background(), `UPDATE metadata SET value=? WHERE key='login_limits'`, stored))
		requireUnreadable(t, adminAPIRequest(t, http.MethodGet, server.URL+"/api/v1/settings", nil, "admin-password"), "login_limits", "login_limits")
	}
}

// The Access tab saves the login limits in the units it shows them in,
// summarizes them, and warns when the new limits are looser.
func TestAccessTabSavesTheLoginLimits(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, true, state.ConfirmEveryTime)
	browser := openConfirmationBrowser(t, server, true)
	refused := browser.post("/settings/access", url.Values{
		"action": {webui.ActionSaveLoginLimits}, "login_attempts": {"0"}, "login_window": {"30"}, "login_window_unit": {"s"},
		"login_pause": {"2"}, "login_pause_unit": {"h"}, "admin_password": {"admin-password"},
	})
	if refused.status != http.StatusUnprocessableEntity || !strings.Contains(refused.body, enText(webui.MsgLoginLimitsInvalid)) || !strings.Contains(refused.body, `name="login_pause" type="text"`) {
		t.Fatalf("refused save status=%d", refused.status)
	}
	if saved, err := fixture.store.LoginLimits(context.Background()); err != nil || saved != state.DefaultLoginLimits {
		t.Fatalf("a refused save changed the limits: %+v %v", saved, err)
	}
	result := browser.post("/settings/access", url.Values{
		"action": {webui.ActionSaveLoginLimits}, "login_attempts": {"8"}, "login_window": {"30"}, "login_window_unit": {"min"},
		"login_pause": {"1"}, "login_pause_unit": {"h"}, "admin_password": {"admin-password"},
	})
	if result.status != http.StatusSeeOther || !strings.Contains(result.header.Get("Location"), "notice=login_limits_looser") {
		t.Fatalf("save status=%d location=%q", result.status, result.header.Get("Location"))
	}
	if saved, err := fixture.store.LoginLimits(context.Background()); err != nil || saved != (state.LoginLimits{Attempts: 8, Window: 30 * time.Minute, Pause: time.Hour}) {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	browser.adminSignIn()
	page := browser.get("/settings/access")
	for _, want := range []string{`name="login_attempts" type="text"`, `data-saved="8"`, `data-saved="30"`, "8 wrong passwords within 30 minutes pause the address for 1 hour."} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("Access tab lacks %q:\n%s", want, page.body)
		}
	}
}

// sameSiteOf returns the SameSite attribute each cookie of header sets.
func sameSiteOf(header http.Header) map[string]http.SameSite {
	found := map[string]http.SameSite{}
	for _, cookie := range (&http.Response{Header: header}).Cookies() {
		found[cookie.Name] = cookie.SameSite
	}
	return found
}

// The shared sign-in cookie follows the cross-site link choice; the
// administrator and form token cookies stay Strict. Saving the choice
// applies it to this browser's sign-in at once.
func TestCrossSiteLinksChooseTheSharedSignInCookie(t *testing.T) {
	_, server, _ := newConfirmationFixture(t, true, state.ConfirmEveryTime)
	signIn := func() (*confirmationBrowser, http.SameSite) {
		client, jar := newBrowserClient(t)
		browser := &confirmationBrowser{t: t, server: server, client: client, jar: jar}
		browser.get("/login")
		result := browserForm(t, client, server.URL+"/login", url.Values{"csrf": {browser.cookie(preauthCookie)}, "password": {"shared-password"}, "next": {"/"}}, server.URL)
		if result.status != http.StatusSeeOther {
			t.Fatalf("sign-in status=%d", result.status)
		}
		return browser, sameSiteOf(result.header)[generalCookie]
	}
	browser, sameSite := signIn()
	if sameSite != http.SameSiteStrictMode {
		t.Fatalf("default sign-in cookie SameSite=%v", sameSite)
	}
	saved := browser.post("/settings/access", url.Values{"action": {webui.ActionSaveCrossSite}, "cross_site_links": {"lax"}, "admin_password": {"admin-password"}})
	if saved.status != http.StatusSeeOther || sameSiteOf(saved.header)[generalCookie] != http.SameSiteLaxMode {
		t.Fatalf("saving Keep the sign-in status=%d cookies=%v", saved.status, sameSiteOf(saved.header))
	}
	if _, sameSite := signIn(); sameSite != http.SameSiteLaxMode {
		t.Fatalf("sign-in after saving Keep the sign-in SameSite=%v", sameSite)
	}
	browser.get("/admin/login")
	admin := browserForm(t, browser.client, server.URL+"/admin/login", url.Values{"csrf": {browser.cookie(preauthCookie)}, "admin_password": {"admin-password"}, "next": {"/"}}, server.URL)
	if got := sameSiteOf(admin.header); got[adminCookie] != http.SameSiteStrictMode || got[preauthCookie] != http.SameSiteStrictMode {
		t.Fatalf("administrator sign-in cookies=%v", got)
	}
	if status, _, settings := settingsAPI(t, server.URL, http.MethodGet, nil); status != http.StatusOK || settings["cross_site_links"] != "lax" {
		t.Fatalf("GET status=%d settings=%v", status, settings)
	}
	if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"cross_site_links": "none"}); status != http.StatusBadRequest || code != "invalid_settings" {
		t.Fatalf("PATCH none status=%d code=%s", status, code)
	}
}

// A cross-site choice that cannot be read stops shared sign-ins with the
// setting named, while the administrator can still sign in to set it.
func TestUnreadableCrossSiteChoiceStopsSharedSignIn(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, true, state.ConfirmEveryTime)
	noErr(t, fixture.store.Exec(context.Background(), `INSERT INTO metadata(key,value) VALUES('cross_site_links','none')`))
	client, jar := newBrowserClient(t)
	browser := &confirmationBrowser{t: t, server: server, client: client, jar: jar}
	browser.get("/login")
	result := browserForm(t, client, server.URL+"/login", url.Values{"csrf": {browser.cookie(preauthCookie)}, "password": {"shared-password"}, "next": {"/"}}, server.URL)
	if result.status != http.StatusConflict || browser.cookie(generalCookie) != "" || !strings.Contains(result.body, enText(webui.MsgCrossSiteUnreadableIn)) {
		t.Fatalf("shared sign-in status=%d", result.status)
	}
	browser.adminSignIn()
	page := browser.get("/settings/access")
	if !strings.Contains(page.body, `name="cross_site_links" data-saved=""`) {
		t.Fatalf("Access tab does not offer to set it again:\n%s", page.body)
	}
}

// The administrator confirmation of every browser form names unreadable
// login limits, as the sign-in page does, and the right password still
// confirms, so the Access tab can set them again.
func TestAdministratorConfirmationNamesUnreadableLoginLimits(t *testing.T) {
	fixture := newAPIFixture(t, false)
	askEveryTime(t, fixture.app)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	noErr(t, fixture.store.Exec(t.Context(), `INSERT INTO metadata(key,value) VALUES('login_limits','{"attempts":null}')`))
	message := html.EscapeString(enText(webui.MsgLoginLimitsUnreadable))
	settings := url.Values{
		"csrf": {adminTestCSRF}, "action": {webui.ActionSaveLoginLimits}, "login_attempts": {"4"}, "login_window": {"10"}, "login_window_unit": {"min"},
		"login_pause": {"15"}, "login_pause_unit": {"min"}, "admin_password": {"not-it"},
	}
	for _, refused := range []struct {
		path   string
		values url.Values
	}{
		{"/settings/access", settings},
		{"/repositories/project/delete", url.Values{"csrf": {adminTestCSRF}, "mode": {"keep_files"}, "confirm_name": {"project"}, "admin_password": {"not-it"}}},
	} {
		result := browserForm(t, client, server.URL+refused.path, refused.values, server.URL)
		if result.status != http.StatusConflict || !strings.Contains(result.body, message) {
			t.Fatalf("%s with a wrong password status=%d:\n%s", refused.path, result.status, result.body)
		}
	}
	assertRepositoryIntact(t, fixture, "a refused confirmation")
	settings.Set("admin_password", "admin-password")
	if result := browserForm(t, client, server.URL+"/settings/access", settings, server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("setting the limits again status=%d", result.status)
	}
	if saved, err := fixture.store.LoginLimits(t.Context()); err != nil || saved != state.DefaultLoginLimits {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
}

// With open access and "Do not ask", the shared-password form compares the
// candidate with the administrator password without a typed one. That
// comparison is a counted administrator check: ordinary candidates are
// accepted and counted, the administrator password is still refused as a
// shared password, and once failures reach the server-wide cap the form
// answers 429 with Retry-After instead of an answer, until reset-admin.
func TestSharedPasswordFormCountsItsAdministratorComparison(t *testing.T) {
	fixture, server, clock := newConfirmationFixture(t, false, state.ConfirmNever)
	ctx := context.Background()
	browser := openConfirmationBrowser(t, server, false)
	// candidate posts a shared password and answers its status. A refused
	// candidate must leave no shared password; an accepted one is undone so
	// the next needs no sign-in.
	candidate := func(password string, want int) browserHTTPResult {
		t.Helper()
		result := browser.post("/settings/access", url.Values{"action": {webui.ActionSaveAccess}, "access_mode": {"password"}, "access_password": {password}})
		if result.status != want {
			t.Fatalf("candidate %q: status=%d, want %d", password, result.status, want)
		}
		hash, err := fixture.store.PasswordHash(ctx, "access")
		noErr(t, err)
		if saved := want == http.StatusSeeOther; saved != (hash != "") {
			t.Fatalf("candidate %q: shared password saved=%v after status %d", password, hash != "", result.status)
		}
		if want == http.StatusSeeOther {
			noErr(t, fixture.store.DisableAccessPassword(ctx))
		}
		return result
	}
	candidate("admin-password", http.StatusUnprocessableEntity)
	candidate("shared-candidate-1", http.StatusSeeOther)
	// One failure so far, and the cap is 20 under the default limits.
	for client := range 18 {
		noErr(t, fixture.store.RecordFailedAttempt(ctx, "admin", fmt.Sprintf("192.0.2.%d", client+1), clock.Now()))
	}
	candidate("shared-candidate-2", http.StatusSeeOther)
	for _, password := range []string{"shared-candidate-3", "admin-password"} {
		if retry := candidate(password, http.StatusTooManyRequests).header.Get("Retry-After"); retry != "900" {
			t.Fatalf("%q during the pause: Retry-After=%q", password, retry)
		}
	}
	encoded, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, fixture.store.SetAdminPassword(ctx, encoded))
	candidate("shared-candidate-4", http.StatusSeeOther)
}
