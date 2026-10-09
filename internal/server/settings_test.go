package server

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func TestChangingSharedPasswordRevokesGeneralSessions(t *testing.T) {
	for _, check := range []struct {
		name, password string
		choice         state.AdminConfirmation
		protected      bool
		remembered     bool
		keep           bool
		refuseSession  bool
	}{
		{"remembered change", "", state.DefaultAdminConfirmation, true, true, true, false},
		{"remembered enable", "", state.DefaultAdminConfirmation, false, true, true, false},
		{"every time change", "admin-password", state.ConfirmEveryTime, true, false, true, false},
		{"every time enable", "admin-password", state.ConfirmEveryTime, false, false, true, false},
		{"unconfirmed change", "", state.ConfirmNever, true, false, false, false},
		{"session write refused", "", state.DefaultAdminConfirmation, true, true, false, true},
	} {
		t.Run(check.name, func(t *testing.T) {
			fixture, server, clock := newConfirmationFixture(t, check.protected, check.choice)
			ctx := context.Background()
			browser := openConfirmationBrowser(t, server, check.protected)
			other := openConfirmationBrowser(t, server, check.protected)
			if check.remembered {
				browser.adminSignIn()
				other.adminSignIn()
			}
			browser.get("/settings/access")
			oldToken := browser.cookie(generalCookie)
			previous, _, err := fixture.store.Session(ctx, oldToken, "general", clock.Now())
			noErr(t, err)
			if check.refuseSession {
				refuseWrites(t, fixture.store, "refuse_general_session", "INSERT ON sessions")
			}
			clock.Add(time.Minute)
			result := browser.post("/settings/access", url.Values{
				"action": {webui.ActionSaveAccess}, "access_mode": {"password"},
				"admin_password": {check.password}, "access_password": {"new-shared-password"},
			})
			if result.status != http.StatusSeeOther {
				t.Fatalf("shared password save status=%d", result.status)
			}
			encoded, err := fixture.store.PasswordHash(ctx, "access")
			noErr(t, err)
			if auth.CheckPassword(encoded, "shared-password") || !auth.CheckPassword(encoded, "new-shared-password") {
				t.Fatal("shared password change was not persisted")
			}
			if _, ok, err := fixture.store.Session(ctx, oldToken, "general", clock.Now()); err != nil || ok {
				t.Fatalf("old general token survived: ok=%v err=%v", ok, err)
			}
			if result := other.get("/"); result.status != http.StatusSeeOther || !strings.HasPrefix(result.header.Get("Location"), "/login?") {
				t.Fatalf("other browser kept ordinary access: status=%d", result.status)
			}
			want := http.StatusSeeOther
			if check.keep {
				want = http.StatusOK
				current, ok, err := fixture.app.Auth.ValidateSession(ctx, browser.cookie(generalCookie), "general")
				if err != nil || !ok {
					t.Fatalf("replacement session ok=%v err=%v", ok, err)
				}
				expires := previous.Expires
				if expires.IsZero() {
					expires = clock.Now().Add(state.DefaultGeneralSession.Length())
				}
				if !current.Expires.Equal(expires) || current.CSRF == previous.CSRF {
					t.Fatalf("replacement expiry=%s want=%s, csrf rotated=%v", current.Expires, expires, current.CSRF != previous.CSRF)
				}
			} else if !strings.HasPrefix(result.header.Get("Location"), "/login?notice=access_password_saved&") {
				t.Fatalf("saved password is not confirmed at sign-in: location=%q", result.header.Get("Location"))
			}
			if result := browser.get("/"); result.status != want {
				t.Fatalf("changing browser ordinary access status=%d want=%d", result.status, want)
			}
		})
	}
}

func TestFreshOpenSettingsUsesStatelessCSRFForPostAndLogout(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	adminHash := fixturePasswordHash(t, "admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, false))
	app.Repositories.SetRoot(canonical)
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)

	body, status := dashboardGET(t, client, server.URL+"/settings")
	if status != http.StatusOK {
		t.Fatalf("fresh settings status=%d", status)
	}
	token := cookieValue(t, jar, server.URL, generalCookie)
	if token == "" || !strings.Contains(body, `name="csrf" value="`+token+`"`) {
		t.Fatal("fresh open-mode settings form did not receive its stateless CSRF token")
	}
	if _, ok, err := store.Session(context.Background(), token, "general", time.Now()); err != nil || ok {
		t.Fatalf("fresh open-mode settings created a DB session: ok=%v err=%v", ok, err)
	}
	response := request(t, client, http.MethodPost, server.URL+"/settings", url.Values{
		"csrf": {token}, "action": {webui.ActionAcknowledgeInsecure}, "insecure_ack": {"1"}, "admin_password": {"admin-password"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("valid stateless settings POST status=%d", response.StatusCode)
	}
	response = request(t, client, http.MethodPost, server.URL+"/logout", url.Values{"csrf": {token}}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("open-mode logout status=%d", response.StatusCode)
	}
	body, status = dashboardGET(t, client, server.URL+"/settings")
	newToken := cookieValue(t, jar, server.URL, generalCookie)
	if status != http.StatusOK || newToken == "" || newToken == token || !strings.Contains(body, `name="csrf" value="`+newToken+`"`) {
		t.Fatalf("settings did not issue a fresh stateless token after logout: status=%d", status)
	}
}

func TestInsecureAcknowledgementRequiresCurrentAdminPassword(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	adminHash := fixturePasswordHash(t, "admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, false))
	app.Repositories.SetRoot(canonical)
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	response := request(t, client, http.MethodGet, server.URL+"/settings", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status=%d", response.StatusCode)
	}
	generalToken := cookieValue(t, jar, server.URL, generalCookie)
	if _, ok, err := store.Session(context.Background(), generalToken, "general", time.Now()); err != nil || ok {
		t.Fatalf("open-mode request unexpectedly persisted a general session: ok=%v err=%v", ok, err)
	}
	values := url.Values{
		"csrf": {generalToken}, "action": {webui.ActionAcknowledgeInsecure}, "insecure_ack": {"1"},
	}
	response = request(t, client, http.MethodPost, server.URL+"/settings", values, server.URL)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("acknowledgement without administrator password status=%d", response.StatusCode)
	}
	settings, _ := store.Settings(context.Background())
	if settings.InsecureHTTPAccepted {
		t.Fatal("insecure HTTP was acknowledged without administrator confirmation")
	}
	values.Set("admin_password", "admin-password")
	response = request(t, client, http.MethodPost, server.URL+"/settings", values, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("acknowledgement with administrator password status=%d", response.StatusCode)
	}
	settings, _ = store.Settings(context.Background())
	if !settings.InsecureHTTPAccepted {
		t.Fatal("confirmed insecure HTTP acknowledgement was not persisted")
	}
}

// settingsGroup returns the markup of one Settings group.
func settingsGroup(t *testing.T, body, group string) string {
	t.Helper()
	start := strings.Index(body, `id="grp-`+group+`"`)
	if start < 0 {
		t.Fatalf("the page has no %s group", group)
	}
	end := strings.Index(body[start:], "</section>")
	return body[start : start+end]
}

// Every Settings change redirects to its group on its tab, with a notice
// that the group shows. The page used to replace the notice from the
// address with the form's own (empty) notices, so a successful change was
// never confirmed.
func TestSettingsChangesConfirmTheSave(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	adminHash := fixturePasswordHash(t, "admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, false))
	app.Repositories.SetRoot(canonical)
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	if _, status := dashboardGET(t, client, server.URL+"/settings"); status != http.StatusOK {
		t.Fatalf("settings status=%d", status)
	}
	token := cookieValue(t, jar, server.URL, generalCookie)
	saved := webui.Text(webui.LangEN, webui.MsgSettingsSaved)
	if body, _ := dashboardGET(t, client, server.URL+"/settings"); strings.Contains(body, saved) {
		t.Fatal("a plain visit claims that settings were saved")
	}

	admin := "admin-password"
	for _, test := range []struct {
		values   url.Values
		location string
		group    string
		notice   webui.MessageCode
	}{
		{url.Values{"action": {webui.ActionAcknowledgeInsecure}, "insecure_ack": {"1"}},
			"/settings/network?notice=insecure_acknowledged#grp-connection", "connection", webui.MsgSettingsAckDone},
		{url.Values{"action": {webui.ActionSetUpdateCheck}, "update_check": {"off"}},
			"/settings?notice=settings_saved#grp-update", "update", webui.MsgSettingsSaved},
		{url.Values{"action": {webui.ActionChangeAdminPassword}, "new_admin_password": {"new-admin-password"}},
			"/settings/access?notice=admin_password_changed#grp-admin", "admin", webui.MsgSettingsAdminChanged},
	} {
		values := test.values
		values.Set("csrf", token)
		values.Set("admin_password", admin)
		response := request(t, client, http.MethodPost, server.URL+"/settings", values, server.URL)
		location := response.Header.Get("Location")
		if response.StatusCode != http.StatusSeeOther || location != test.location {
			t.Fatalf("%s: status=%d location=%q", values.Get("action"), response.StatusCode, location)
		}
		body, status := dashboardGET(t, client, server.URL+location)
		if status != http.StatusOK || !strings.Contains(settingsGroup(t, body, test.group), html.EscapeString(webui.Text(webui.LangEN, test.notice))) {
			t.Errorf("%s: the saved change is not confirmed in its group (status %d)", values.Get("action"), status)
		}
		if strings.Contains(body, `class="notices"`) {
			t.Errorf("%s: the confirmation is also shown above the page", values.Get("action"))
		}
		if values.Get("action") == webui.ActionChangeAdminPassword {
			admin = "new-admin-password"
		}
	}

	// Korean readers get the same confirmation.
	afterAction(t, jar, server.URL, "settings_saved")
	body, _ := dashboardGET(t, client, server.URL+"/settings?notice=settings_saved&lang=ko")
	if !strings.Contains(body, webui.Text(webui.LangKO, webui.MsgSettingsSaved)) {
		t.Error("the Korean page does not confirm the save")
	}
}

func TestAccessPasswordChangesEndOnTheExpectedPage(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	askEveryTime(t, app)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	accessHash := fixturePasswordHash(t, "shared-password")
	adminHash := fixturePasswordHash(t, "admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "password", accessHash, adminHash, false))
	app.Repositories.SetRoot(canonical)
	settings, _ := store.Settings(context.Background())
	noErr(t, store.CreateSession(context.Background(), "general-token", "general", "csrf-token", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: generalCookie, Value: "general-token", Path: "/"}})
	saved := webui.Text(webui.LangEN, webui.MsgSettingsAccessDisabled)

	response := request(t, client, http.MethodPost, server.URL+"/settings", url.Values{
		"csrf": {"csrf-token"}, "action": {webui.ActionDisableAccessPassword}, "admin_password": {"admin-password"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("disable status=%d", response.StatusCode)
	}
	body, status := dashboardGET(t, client, server.URL+response.Header.Get("Location"))
	if status != http.StatusOK || !strings.Contains(body, saved) {
		t.Fatalf("disabling the shared password is not confirmed (status %d)", status)
	}

	token := cookieValue(t, jar, server.URL, generalCookie)
	response = request(t, client, http.MethodPost, server.URL+"/settings", url.Values{
		"csrf": {token}, "action": {webui.ActionEnableAccessPassword}, "admin_password": {"admin-password"},
		"access_password": {"another-shared-password"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("enable status=%d", response.StatusCode)
	}
	if location := response.Header.Get("Location"); location != "/settings/access?notice=access_enabled#grp-access" {
		t.Fatalf("after enabling the shared password location=%q, want Settings with the confirmation", location)
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		afterAction(t, jar, server.URL, "access_enabled")
		body, status := dashboardGET(t, client, server.URL+"/settings/access?notice=access_enabled&lang="+string(lang))
		if status != http.StatusOK || !strings.Contains(body, webui.Text(lang, webui.MsgSettingsAccessEnabled)) {
			t.Fatalf("%s: Settings does not confirm the saved shared password (status %d)", lang, status)
		}
	}

	// An administrator session keeps Settings open, so the change is confirmed
	// there.
	noErr(t, store.CreateSession(context.Background(), "admin-token", "admin", "admin-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	jar.SetCookies(parsed, []*http.Cookie{{Name: adminCookie, Value: "admin-token", Path: "/"}})
	response = request(t, client, http.MethodPost, server.URL+"/settings", url.Values{
		"csrf": {"admin-csrf"}, "action": {webui.ActionChangeAccessPassword}, "admin_password": {"admin-password"},
		"access_password": {"third-shared-password"},
	}, server.URL)
	if location := response.Header.Get("Location"); response.StatusCode != http.StatusSeeOther || location != "/settings/access?notice=access_changed#grp-access" {
		t.Fatalf("change with an administrator session status=%d location=%q", response.StatusCode, location)
	}
}
