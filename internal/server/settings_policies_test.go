package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// settingsAPI sends a request to /api/v1/settings with the administrator
// password and decodes a successful answer.
func settingsAPI(t *testing.T, server string, method string, body any) (int, string, map[string]any) {
	t.Helper()
	response := adminAPIRequest(t, method, server+"/api/v1/settings", body, "admin-password")
	defer response.Body.Close()
	var decoded struct {
		OK       bool           `json:"ok"`
		Settings map[string]any `json:"settings"`
		Error    struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	noErr(t, json.NewDecoder(response.Body).Decode(&decoded))
	return response.StatusCode, decoded.Error.Code, decoded.Settings
}

// A chosen sign-in length applies to sign-ins after it is saved; a browser
// already signed in keeps its end.
func TestSessionLengthAppliesToLaterSignIns(t *testing.T) {
	fixture, server, clock := newConfirmationFixture(t, true, state.ConfirmEveryTime)
	ctx := context.Background()
	before := openConfirmationBrowser(t, server, true)
	requireSaved(t, "session length", before.post("/settings/access", url.Values{
		"action": {webui.ActionSaveSession}, "general_session": {"7d"}, "admin_password": {"admin-password"},
	}))
	after := openConfirmationBrowser(t, server, true)
	for _, check := range []struct {
		browser *confirmationBrowser
		life    time.Duration
	}{{before, 12 * time.Hour}, {after, 7 * 24 * time.Hour}} {
		session, ok, err := fixture.store.Session(ctx, check.browser.cookie(generalCookie), "general", clock.Now())
		if err != nil || !ok || !session.Expires.Equal(clock.Now().Add(check.life)) {
			t.Fatalf("session ok=%v err=%v ends %v, want %v", ok, err, session.Expires, clock.Now().Add(check.life))
		}
	}
	if page := after.get("/settings/access"); !strings.Contains(page.body, `name="general_session" data-saved="7d"`) {
		t.Fatalf("Access does not show the saved length:\n%s", page.body)
	}
}

// The owner API reads and changes the sign-in length with the
// administrator password, refuses an unknown length without saving
// anything, and asks for the password without it.
func TestSettingsAPIChangesTheSessionLength(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	if status, _, settings := settingsAPI(t, server.URL, http.MethodGet, nil); status != http.StatusOK || settings["session"] != "12h" {
		t.Fatalf("GET status=%d settings=%v", status, settings)
	}
	if status, code, _ := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"session": "2d"}); status != http.StatusBadRequest || code != "invalid_settings" {
		t.Fatalf("unknown length status=%d code=%s", status, code)
	}
	if status, _, settings := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"session": "30d"}); status != http.StatusOK || settings["session"] != "30d" {
		t.Fatalf("PATCH status=%d settings=%v", status, settings)
	}
	if saved, err := fixture.store.GeneralSession(context.Background()); err != nil || saved != state.Session30Days {
		t.Fatalf("saved=%q err=%v", saved, err)
	}
	response := apiRequest(t, http.MethodPatch, server.URL+"/api/v1/settings", map[string]any{"session": "1h"}, "shared-password", "")
	if status, code := checkStatus(t, response); status != http.StatusUnauthorized || code != "admin_authentication_required" {
		t.Fatalf("shared password status=%d code=%s", status, code)
	}
}

// A saved length that cannot be read is an error where it applies, never
// the default: a sign-in fails and the API says to set it again. Access
// still opens and offers the default to save in its place.
func TestUnreadableSessionLengthIsAnErrorUntilSetAgain(t *testing.T) {
	fixture, server, _ := newConfirmationFixture(t, true, state.ConfirmEveryTime)
	noErr(t, fixture.store.Exec(context.Background(), `INSERT INTO metadata(key,value) VALUES('general_session_seconds','5')`))
	browser := openConfirmationBrowser(t, server, false)
	browser.get("/login")
	result := browserForm(t, browser.client, server.URL+"/login", url.Values{"csrf": {browser.cookie(preauthCookie)}, "password": {"shared-password"}, "next": {"/"}}, server.URL)
	if result.status == http.StatusSeeOther || browser.cookie(generalCookie) != "" {
		t.Fatalf("sign-in with an unreadable length status=%d", result.status)
	}
	if status, code, _ := settingsAPI(t, server.URL, http.MethodGet, nil); status != http.StatusConflict || code != "setting_unreadable" {
		t.Fatalf("GET status=%d code=%s", status, code)
	}
	browser.adminSignIn()
	page := browser.get("/settings/access")
	if page.status != http.StatusOK || !strings.Contains(page.body, enText(webui.MsgPolicyUnreadable)) || !strings.Contains(page.body, `name="general_session" data-saved=""`) {
		t.Fatalf("Access status=%d:\n%s", page.status, page.body)
	}
	if status, _, settings := settingsAPI(t, server.URL, http.MethodPatch, map[string]any{"session": "12h"}); status != http.StatusOK || settings["session"] != "12h" {
		t.Fatalf("PATCH status=%d settings=%v", status, settings)
	}
}
