package server

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// damageSession makes the stored session of token unreadable, as a damaged
// row in the state database is, while every other session still reads.
func damageSession(t *testing.T, store *state.Store, token, kind string) {
	t.Helper()
	noErr(t, store.Exec(context.Background(), `UPDATE sessions SET expires_at='unreadable' WHERE kind='`+kind+`'`))
	if _, _, err := store.Session(context.Background(), token, kind, time.Now()); err == nil {
		t.Fatalf("the %s session still reads", kind)
	}
}

// A session that could not be read may be valid, so the request is answered
// as unavailable and logged once. It is not sent to sign in, refused as a
// forgery, or challenged for a password. Only an absent session is.
func TestSessionThatCannotBeReadIsUnavailable(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	ctx := context.Background()
	settings, err := fixture.store.Settings(ctx)
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(ctx, "general-session", "general", "general-csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	noErr(t, fixture.store.CreateSession(ctx, "admin-session", "admin", "admin-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	client, jar := newBrowserClient(t)
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: generalCookie, Value: "general-session", Path: "/"}})
	damageSession(t, fixture.store, "general-session", "general")
	damageSession(t, fixture.store, "admin-session", "admin")
	serverLog := captureServerLog(t)

	for _, check := range []struct {
		name   string
		do     func() (int, http.Header)
		step   string
		prefix string
	}{
		{"overview", func() (int, http.Header) {
			result := browserGET(t, client, server.URL+"/")
			return result.status, result.header
		}, "session read", "GET /"},
		{"settings", func() (int, http.Header) {
			result := browserGET(t, client, server.URL+"/settings")
			return result.status, result.header
		}, "session read", "GET /settings"},
		{"sign-out", func() (int, http.Header) {
			result := browserForm(t, client, server.URL+"/logout", url.Values{"csrf": {"general-csrf"}}, server.URL)
			return result.status, result.header
		}, "CSRF check", "POST /logout"},
		{"admin API", func() (int, http.Header) {
			response := adminSessionRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/project/helper-credentials", nil, "admin-csrf", "")
			status, code := checkStatus(t, response)
			if code != "state_unavailable" {
				t.Errorf("admin API code=%q", code)
			}
			return status, response.Header
		}, "session read", "GET /api/v1/repositories/project/helper-credentials"},
	} {
		endFailureWindows()
		since := len(serverLog.String())
		status, header := check.do()
		if status != http.StatusServiceUnavailable || header.Get("Location") != "" || header.Get("WWW-Authenticate") != "" {
			t.Errorf("%s status=%d location=%q challenge=%q, want 503 alone", check.name, status, header.Get("Location"), header.Get("WWW-Authenticate"))
		}
		lines := loggedFailures(serverLog, since)
		checkLoggedSteps(t, check.name, lines, check.step)
		if len(lines) == 1 && !strings.Contains(lines[0], check.prefix+": ") {
			t.Errorf("%s logged %q", check.name, lines[0])
		}
	}
	if logged := serverLog.String(); strings.Contains(logged, "general-session") || strings.Contains(logged, "admin-session") || strings.Contains(logged, "-csrf") {
		t.Fatalf("the log names a session token:\n%s", logged)
	}

	// A browser without a session is still sent to sign in, and a token that
	// matches no session is still a forgery.
	stranger, _ := newBrowserClient(t)
	if result := browserGET(t, stranger, server.URL+"/"); result.status != http.StatusSeeOther || !strings.HasPrefix(result.header.Get("Location"), "/login") {
		t.Fatalf("no session status=%d location=%q", result.status, result.header.Get("Location"))
	}
	if result := browserForm(t, stranger, server.URL+"/logout", url.Values{"csrf": {"general-csrf"}}, server.URL); result.status != http.StatusForbidden {
		t.Fatalf("sign-out without a session status=%d", result.status)
	}
	response := sendJSON(t, http.MethodGet, server.URL+"/api/v1/repositories/project/helper-credentials", nil, adminCookieValue("unknown-session"), header(csrfHeader, "admin-csrf"))
	if status, code := checkStatus(t, response); status != http.StatusUnauthorized || code != "admin_authentication_required" || response.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("unknown admin session status=%d code=%q", status, code)
	}
}

// A form token that matches a session that was read is valid even when
// another session of the same browser could not be read.
func TestCSRFMatchingAReadSessionIsValid(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	ctx := context.Background()
	settings, err := fixture.store.Settings(ctx)
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(ctx, "general-session", "general", "general-csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	noErr(t, fixture.store.CreateSession(ctx, "admin-session", "admin", "admin-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	client, jar := newBrowserClient(t)
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{
		{Name: generalCookie, Value: "general-session", Path: "/"}, {Name: adminCookie, Value: "admin-session", Path: "/"},
	})
	damageSession(t, fixture.store, "general-session", "general")
	result := browserForm(t, client, server.URL+"/admin/logout", url.Values{"csrf": {"admin-csrf"}}, server.URL)
	if result.status != http.StatusSeeOther {
		t.Fatalf("admin sign-out status=%d", result.status)
	}
	if _, live, err := fixture.store.Session(ctx, "admin-session", "admin", time.Now()); err != nil || live {
		t.Fatalf("admin session live=%v err=%v", live, err)
	}
}

// A setup session that could not be read may be valid, on a known Host and
// on the unknown Host it is bound to alike, so setup answers unavailable
// instead of offering the setup link again, refusing the form as an ended
// session, or refusing the Host.
func TestSetupSessionThatCannotBeReadIsUnavailable(t *testing.T) {
	for _, host := range []string{"", "192.168.1.20:7720"} {
		t.Run(map[string]string{"": "known Host", "192.168.1.20:7720": "unknown Host"}[host], func(t *testing.T) {
			app, store, repositoryRoot := newTestApp(t)
			noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
			browser := newHostBrowser(t, app, host)
			browser.redeem("synthetic-owner-token")
			damageSession(t, store, cookieValue(t, browser.jar, browser.server, setupCookie), "setup")
			serverLog := captureServerLog(t)

			if status, page := browser.do(http.MethodGet, "/setup", nil); status != http.StatusServiceUnavailable {
				t.Errorf("setup page status=%d, redemption form shown=%v", status, strings.Contains(page, `action="/setup/redeem"`))
			}
			endFailureWindows()
			if status := browser.status(http.MethodPost, "/setup", url.Values{
				"csrf": {"any"}, "storage_path": {repositoryRoot}, "access_mode": {"open"}, "admin_password": {"admin-password-one"},
			}); status != http.StatusServiceUnavailable {
				t.Errorf("setup form status=%d", status)
			}
			if lines := loggedFailures(serverLog, 0); len(lines) != 2 {
				t.Errorf("logged %d lines, want one for each request:\n%s", len(lines), strings.Join(lines, "\n"))
			}
			if settings, err := store.Settings(context.Background()); err != nil || settings.Initialized {
				t.Fatalf("setup completed=%v err=%v", settings.Initialized, err)
			}
		})
	}
}

// A new shared password ends this browser's general session, so after the
// save the confirmation goes to sign-in unless an administrator session keeps
// Settings open. An administrator session that could not be read may have
// ended: the confirmation goes to sign-in, where it is shown either way, and
// the read failure is logged instead of passing as no session.
func TestSavedPasswordConfirmationSurvivesAnUnreadableAdminSession(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	ctx := context.Background()
	settings, err := fixture.store.Settings(ctx)
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(ctx, "general-session", "general", "general-csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	noErr(t, fixture.store.CreateSession(ctx, "admin-session", "admin", "admin-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	client, jar := newBrowserClient(t)
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{
		{Name: generalCookie, Value: "general-session", Path: "/"}, {Name: adminCookie, Value: "admin-session", Path: "/"},
	})
	damageSession(t, fixture.store, "admin-session", "admin")
	serverLog := captureServerLog(t)
	result := browserForm(t, client, server.URL+"/settings", url.Values{
		"csrf": {"general-csrf"}, "action": {webui.ActionChangeAccessPassword}, "admin_password": {"admin-password"},
		"access_password": {"another-shared-password"},
	}, server.URL)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/login?notice=access_password_saved&next=%2Fsettings" {
		t.Fatalf("save status=%d location=%q", result.status, result.header.Get("Location"))
	}
	checkLoggedSteps(t, "save", loggedFailures(serverLog, 0), "session read")
}

// Once setup is complete, the setup form refuses an unknown Host whatever its
// setup session is, so a setup session that could not be read does not
// change that answer.
func TestCompletedSetupRefusesAnUnknownHostWithoutReadingItsSession(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	browser := newHostBrowser(t, app, "192.168.1.20:7720")
	browser.redeem("synthetic-owner-token")
	token := cookieValue(t, browser.jar, browser.server, setupCookie)
	adminHash := fixturePasswordHash(t, "admin-password")
	noErr(t, store.CompleteSetup(context.Background(), repositoryRoot, "open", "", adminHash, true))
	// Completing setup ended the setup session; a damaged copy stays.
	hash := sha256.Sum256([]byte(token))
	noErr(t, store.Exec(context.Background(), `INSERT INTO sessions(token_hash,kind,csrf,version,expires_at) VALUES(?,'setup','any',0,'unreadable')`, hash[:]))
	serverLog := captureServerLog(t)
	if status := browser.status(http.MethodPost, "/setup", url.Values{"csrf": {"any"}}); status != http.StatusMisdirectedRequest {
		t.Fatalf("setup form after setup status=%d, want 421", status)
	}
	if lines := loggedFailures(serverLog, 0); len(lines) != 0 {
		t.Fatalf("logged %q", lines)
	}
}

// A session found ended, by its expiry or by a password change, is not valid
// even when removing it fails. The failed removal is logged, and the browser
// is still sent to sign in.
func TestEndedSessionThatCannotBeRemovedIsLogged(t *testing.T) {
	for _, ended := range []struct {
		name    string
		version int64
		expires time.Time
	}{
		{"expired", 0, time.Now().Add(-time.Minute)},
		{"password changed", -1, time.Now().Add(time.Hour)},
	} {
		t.Run(ended.name, func(t *testing.T) {
			fixture := newAPIFixture(t, true)
			server := serve(t, fixture.app.Handler())
			ctx := context.Background()
			settings, err := fixture.store.Settings(ctx)
			noErr(t, err)
			noErr(t, fixture.store.CreateSession(ctx, "ended-session", "general", "ended-csrf", settings.AccessSessionVersion+ended.version, ended.expires))
			client, jar := newBrowserClient(t)
			parsed, _ := url.Parse(server.URL)
			jar.SetCookies(parsed, []*http.Cookie{{Name: generalCookie, Value: "ended-session", Path: "/"}})
			refuseWrites(t, fixture.store, "keep_sessions", "DELETE ON sessions")
			serverLog := captureServerLog(t)
			result := browserGET(t, client, server.URL+"/repositories/new")
			if result.status != http.StatusSeeOther || !strings.HasPrefix(result.header.Get("Location"), "/login") {
				t.Fatalf("status=%d location=%q", result.status, result.header.Get("Location"))
			}
			lines := loggedFailures(serverLog, 0)
			checkLoggedSteps(t, "ended session", lines, "ended session removal")
			if strings.Contains(serverLog.String(), "ended-session") {
				t.Fatal("the log names the session token")
			}
		})
	}
}
