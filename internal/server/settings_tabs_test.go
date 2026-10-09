package server

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/webui"
)

// settingsGroupForm posts values as the Settings script does, with the
// group header, or as a browser without it when group is "".
func settingsGroupForm(t *testing.T, client *http.Client, base, path, group string, values url.Values) browserHTTPResult {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(values.Encode()))
	noErr(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", base)
	if group != "" {
		request.Header.Set(settingsGroupHeader, group)
	}
	return browserRequest(t, client, request)
}

// Each tab is its own address, reached by a plain link that marks the
// current tab, and shows only its own groups. Other addresses under
// /settings do not exist.
func TestSettingsTabsAreAddresses(t *testing.T) {
	app := newConfiguredApp(t)
	client, base, _, _ := networkSettingsClient(t, app)
	groups := map[string][]string{
		"/settings":              {"update"},
		"/settings/access":       {"access", "admin"},
		"/settings/network":      {"connection", "network", "tailscale"},
		"/settings/repositories": {"repositories"},
		"/settings/storage":      {"storage"},
	}
	for path := range groups {
		body, status := dashboardGET(t, client, base+path)
		if status != http.StatusOK {
			t.Fatalf("%s: status=%d", path, status)
		}
		if !strings.Contains(body, `href="`+path+`" aria-current="page"`) || strings.Count(body, `aria-current="page"`) != 2 {
			t.Errorf("%s: the tab strip does not mark this tab alone as current", path)
		}
		for other, groups := range groups {
			for _, group := range groups {
				if shown := strings.Contains(body, `id="grp-`+group+`"`); shown != (other == path) {
					t.Errorf("%s: the %s group shown=%v", path, group, shown)
				}
			}
		}
	}
	for _, path := range []string{"/settings/", "/settings/general", "/settings/access/extra", "/settings/other"} {
		if _, status := dashboardGET(t, client, base+path); status != http.StatusNotFound {
			t.Errorf("%s: status=%d, want 404", path, status)
		}
	}
}

// The Access group is one choice with an optional new shared password. The
// server turns it into turning the password on, changing it or turning it
// off, and a save that asks for what is saved changes nothing.
func TestSavingTheAccessGroupAsksForOneChange(t *testing.T) {
	app := newConfiguredApp(t)
	askEveryTime(t, app)
	client, base, csrf, _ := networkSettingsClient(t, app)
	save := func(fields url.Values) browserHTTPResult {
		fields.Set("csrf", csrf)
		fields.Set("action", webui.ActionSaveAccess)
		fields.Set("admin_password", "admin-password")
		return browserForm(t, client, base+"/settings/access", fields, base)
	}
	mode := func() string {
		settings, err := app.Store.Settings(context.Background())
		noErr(t, err)
		return settings.AccessMode
	}
	nothing := html.EscapeString(webui.Text(webui.LangEN, webui.MsgSettingsNothing))

	result := save(url.Values{"access_mode": {"open"}})
	if result.status != http.StatusOK || !strings.Contains(settingsGroup(t, result.body, "access"), nothing) || mode() != "open" {
		t.Fatalf("saving the saved choice: status=%d mode=%s", result.status, mode())
	}
	result = save(url.Values{"access_mode": {"sometimes"}})
	if result.status != http.StatusBadRequest || mode() != "open" {
		t.Fatalf("an unknown choice: status=%d mode=%s", result.status, mode())
	}
	// A refused choice keeps what was chosen, never the password.
	result = save(url.Values{"access_mode": {"password"}, "access_password": {"short"}})
	group := settingsGroup(t, result.body, "access")
	if result.status != http.StatusUnprocessableEntity || !strings.Contains(group, `<option value="password"`) ||
		!strings.Contains(strings.Join(strings.Fields(group[strings.Index(group, `<option value="password"`):]), " "), `selected>`) ||
		strings.Contains(result.body, `value="short"`) || mode() != "open" {
		t.Fatalf("a short shared password: status=%d mode=%s", result.status, mode())
	}
	result = save(url.Values{"access_mode": {"password"}, "access_password": {"first-shared-password"}})
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/settings/access?notice=access_enabled#grp-access" || mode() != "password" {
		t.Fatalf("turning the shared password on: status=%d location=%q", result.status, result.header.Get("Location"))
	}

	body, status := dashboardGET(t, client, base+"/settings/access")
	if status != http.StatusOK {
		t.Fatalf("changing browser lost Settings access: status=%d", status)
	}
	csrf = formValue(t, body, "csrf")
	result = save(url.Values{"access_mode": {"password"}, "access_password": {""}})
	if result.status != http.StatusOK || !strings.Contains(settingsGroup(t, result.body, "access"), nothing) {
		t.Fatalf("keeping the shared password: status=%d", result.status)
	}
	hash, err := app.Store.PasswordHash(context.Background(), "access")
	noErr(t, err)
	if !auth.CheckPassword(hash, "first-shared-password") {
		t.Fatal("an empty new password replaced the shared password")
	}
	result = save(url.Values{"access_mode": {"open"}})
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/settings/access?notice=access_disabled#grp-access" || mode() != "open" {
		t.Fatalf("turning the shared password off: status=%d location=%q", result.status, result.header.Get("Location"))
	}
}

// The group header chooses only the form of the answer: a saved change is
// the redirect's address as JSON, with the same result notice kept, and a
// refused change is the same page.
func TestSettingsGroupAnswerIsTheSameWithAndWithoutTheScript(t *testing.T) {
	app := newConfiguredApp(t)
	client, base, csrf, _ := networkSettingsClient(t, app)
	values := func(password, check string) url.Values {
		return url.Values{"csrf": {csrf}, "action": {webui.ActionSetUpdateCheck}, "admin_password": {password}, "update_check": {check}}
	}

	plain := settingsGroupForm(t, client, base, "/settings", "", values("wrong-password", "off"))
	script := settingsGroupForm(t, client, base, "/settings", "update", values("wrong-password", "off"))
	if plain.status != http.StatusUnauthorized || script.status != plain.status ||
		settingsGroup(t, script.body, "update") != settingsGroup(t, plain.body, "update") {
		t.Fatalf("refused: status %d and %d, or different groups", plain.status, script.status)
	}
	// The refused group shows what was sent, not the saved value, and is
	// marked for the script.
	if !strings.Contains(settingsGroup(t, plain.body, "update"), "data-group-refused") || updateSwitchOn(t, plain.body) {
		t.Fatal("the refused group does not keep the switch as it was sent")
	}

	plain = settingsGroupForm(t, client, base, "/settings", "", values("admin-password", "off"))
	const location = "/settings?notice=settings_saved#grp-update"
	if plain.status != http.StatusSeeOther || plain.header.Get("Location") != location {
		t.Fatalf("saved without the script: status=%d location=%q", plain.status, plain.header.Get("Location"))
	}
	script = settingsGroupForm(t, client, base, "/settings", "update", values("admin-password", "on"))
	var answer struct {
		Location string `json:"location"`
	}
	if script.status != http.StatusOK || script.header.Get("Location") != "" ||
		!strings.HasPrefix(script.header.Get("Content-Type"), "application/json") || script.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("saved with the script: status=%d headers=%v", script.status, script.header)
	}
	noErr(t, json.Unmarshal([]byte(script.body), &answer))
	if answer.Location != location {
		t.Fatalf("saved with the script: location=%q", answer.Location)
	}
	if settings, _ := app.Store.Settings(context.Background()); !settings.UpdateCheck {
		t.Fatal("the change sent by the script was not saved")
	}
	// The result notice is kept for the address, as for the redirect.
	body, _ := dashboardGET(t, client, base+answer.Location)
	if !strings.Contains(settingsGroup(t, body, "update"), html.EscapeString(webui.Text(webui.LangEN, webui.MsgSettingsSaved))) {
		t.Fatal("the address the script reads does not confirm the save")
	}
}

// Saving one group changes only that group's settings.
func TestSavingOneSettingsGroupLeavesTheOthers(t *testing.T) {
	app := newConfiguredApp(t)
	client, base, csrf, body := networkSettingsClient(t, app)
	before, err := app.Store.Settings(context.Background())
	noErr(t, err)
	result := browserForm(t, client, base+"/settings/network", saveNetworkForm(csrf, formValue(t, body, "network_revision"), "admin-password", map[string]string{
		"listen": "127.0.0.1:7798",
	}), base)
	if result.status != http.StatusSeeOther {
		t.Fatalf("network save status=%d", result.status)
	}
	after, err := app.Store.Settings(context.Background())
	noErr(t, err)
	if after.AccessMode != before.AccessMode || after.UpdateCheck != before.UpdateCheck || after.InsecureHTTPAccepted != before.InsecureHTTPAccepted {
		t.Fatalf("a network save changed other settings: before %+v after %+v", before, after)
	}
	result = browserForm(t, client, base+"/settings", url.Values{
		"csrf": {csrf}, "action": {webui.ActionSetUpdateCheck}, "admin_password": {"admin-password"},
	}, base)
	if settings, _, _ := savedNetwork(t, app.Store); result.status != http.StatusSeeOther || settings.Listen != "127.0.0.1:7798" {
		t.Fatalf("an update check save: status=%d listen=%q", result.status, settings.Listen)
	}
}

// Save and leave sends a group's page with the address being left for. A
// saved change then continues there; a refused change stays on the tab
// with the error. The script's answer and any address off this site keep
// the tab.
func TestASavedGroupContinuesToThePageBeingLeftFor(t *testing.T) {
	app := newConfiguredApp(t)
	askEveryTime(t, app)
	client, base, csrf, _ := networkSettingsClient(t, app)
	update := func(group, password, check, leave string) browserHTTPResult {
		return settingsGroupForm(t, client, base, "/settings", group, url.Values{
			"csrf": {csrf}, "action": {webui.ActionSetUpdateCheck}, "admin_password": {password}, "update_check": {check}, "leave_to": {leave},
		})
	}
	saved := func() bool {
		settings, err := app.Store.Settings(context.Background())
		noErr(t, err)
		return settings.UpdateCheck
	}
	const tab = "/settings?notice=settings_saved#grp-update"
	onOff := map[bool]string{true: "on", false: "off"}
	before := saved()

	result := update("", "wrong-password", onOff[!before], "/activity")
	if result.status != http.StatusUnauthorized || !strings.Contains(settingsGroup(t, result.body, "update"), "data-group-refused") || saved() != before {
		t.Fatalf("a refused save: status=%d saved=%v", result.status, saved())
	}
	result = update("", "admin-password", onOff[!before], "/activity?lang=ko#top")
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/activity?lang=ko#top" || saved() == before {
		t.Fatalf("a saved change: status=%d location=%q saved=%v", result.status, result.header.Get("Location"), saved())
	}
	for _, leave := range []string{"https://example.test/", "//example.test/", "/\t/example.test", `/\example.test`, "activity", ""} {
		want := !saved()
		result = update("", "admin-password", onOff[want], leave)
		if result.status != http.StatusSeeOther || result.header.Get("Location") != tab || saved() != want {
			t.Fatalf("leaving for %q: status=%d location=%q", leave, result.status, result.header.Get("Location"))
		}
	}
	result = update("update", "admin-password", onOff[!saved()], "/activity")
	var answer struct {
		Location string `json:"location"`
	}
	noErr(t, json.Unmarshal([]byte(result.body), &answer))
	if result.status != http.StatusOK || answer.Location != tab {
		t.Fatalf("the script's answer: status=%d location=%q", result.status, answer.Location)
	}

	result = browserForm(t, client, base+"/settings/access", url.Values{
		"csrf": {csrf}, "action": {webui.ActionSaveAccess}, "admin_password": {"admin-password"},
		"access_mode": {"password"}, "access_password": {"first-shared-password"}, "leave_to": {"/activity"},
	}, base)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/activity" {
		t.Fatalf("a new shared password: status=%d location=%q", result.status, result.header.Get("Location"))
	}
	if result := browserGET(t, client, base+"/activity"); result.status != http.StatusOK {
		t.Fatalf("changing browser lost ordinary access: status=%d", result.status)
	}
}
