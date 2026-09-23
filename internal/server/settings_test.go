package server

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/webui"
)

func TestChangingSharedPasswordRevokesGeneralSessions(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	accessHash, _ := auth.HashPassword("old-shared-password")
	adminHash, _ := auth.HashPassword("admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "password", accessHash, adminHash, true))
	app.Repositories.SetRoot(canonical)
	settings, _ := store.Settings(context.Background())
	noErr(t, store.CreateSession(context.Background(), "general-token", "general", "csrf-token", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: generalCookie, Value: "general-token", Path: "/"}})
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response := request(t, client, http.MethodPost, server.URL+"/settings", url.Values{
		"csrf": {"csrf-token"}, "action": {webui.ActionChangeAccessPassword},
		"admin_password": {"admin-password"}, "access_password": {"new-shared-password"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("change shared password status=%d", response.StatusCode)
	}
	encoded, _ := store.PasswordHash(context.Background(), "access")
	if auth.CheckPassword(encoded, "old-shared-password") || !auth.CheckPassword(encoded, "new-shared-password") {
		t.Fatal("shared password change was not persisted")
	}
	if _, ok, err := store.Session(context.Background(), "general-token", "general", time.Now()); err != nil || ok {
		t.Fatalf("general session survived shared password change: ok=%v err=%v", ok, err)
	}
}

func TestFreshOpenSettingsUsesStatelessCSRFForPostAndLogout(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	adminHash, _ := auth.HashPassword("admin-password")
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
	adminHash, _ := auth.HashPassword("admin-password")
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
