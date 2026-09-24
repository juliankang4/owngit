package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func TestCookieNamesUseOwngitNamespace(t *testing.T) {
	for _, cookie := range []struct {
		name string
		got  string
		want string
	}{
		{"general", generalCookie, "owngit_general"},
		{"admin", adminCookie, "owngit_admin"},
		{"setup", setupCookie, "owngit_setup"},
		{"preauth", preauthCookie, "owngit_preauth"},
		{"language", languageCookie, "owngit_lang"},
	} {
		if cookie.got != cookie.want {
			t.Errorf("%s cookie = %q, want %q", cookie.name, cookie.got, cookie.want)
		}
	}
}

func TestSetupOpenModeRepositoryCreationAndPasswordTransitions(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)

	response := request(t, client, http.MethodGet, server.URL+"/setup", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET setup status=%d", response.StatusCode)
	}
	preauth := cookieValue(t, jar, server.URL, preauthCookie)
	response = request(t, client, http.MethodPost, server.URL+"/setup/redeem", url.Values{
		"csrf": {preauth}, "token": {"synthetic-owner-token"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeem status=%d", response.StatusCode)
	}
	setupToken := cookieValue(t, jar, server.URL, setupCookie)
	setupSession, ok, err := store.Session(context.Background(), setupToken, "setup", time.Now())
	if err != nil || !ok {
		t.Fatalf("setup session ok=%v err=%v", ok, err)
	}
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	unrelated := filepath.Join(repositoryRoot, "owner-notes.txt")
	noErr(t, os.WriteFile(unrelated, []byte("leave this file alone"), 0o600))
	response = request(t, client, http.MethodPost, server.URL+"/setup", url.Values{
		"csrf": {setupSession.CSRF}, "storage_path": {repositoryRoot}, "access_mode": {"open"},
		"admin_password": {"admin-password-one"}, "insecure_ack": {"on"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("complete setup status=%d", response.StatusCode)
	}
	settings, _ := store.Settings(context.Background())
	canonicalRoot, err := filepath.EvalSymlinks(repositoryRoot)
	noErr(t, err)
	if !settings.Initialized || settings.AccessMode != "open" || settings.RepositoryRoot != canonicalRoot {
		t.Fatalf("unexpected completed settings: %+v", settings)
	}
	if content, err := os.ReadFile(unrelated); err != nil || string(content) != "leave this file alone" {
		t.Fatalf("setup changed an unrelated storage file: content=%q err=%v", content, err)
	}
	openGitRequest := httptest.NewRequest(http.MethodGet, "http://localhost/git/project.git/info/refs?service=git-upload-pack", nil)
	if !app.AuthorizeGit(openGitRequest) {
		t.Fatal("password-free mode did not authorize Git discovery")
	}

	response = request(t, client, http.MethodGet, server.URL+"/", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("open dashboard status=%d", response.StatusCode)
	}
	generalToken := cookieValue(t, jar, server.URL, generalCookie)
	if _, ok, err := store.Session(context.Background(), generalToken, "general", time.Now()); err != nil || ok {
		t.Fatalf("open-mode request unexpectedly persisted a general session: ok=%v err=%v", ok, err)
	}
	generalCSRF := generalToken
	settingsBody, settingsStatus := dashboardGET(t, client, server.URL+"/settings")
	if settingsStatus != http.StatusOK || strings.Contains(settingsBody, canonicalRoot) {
		t.Fatalf("general settings leaked owner storage path: status=%d", settingsStatus)
	}
	response = request(t, client, http.MethodGet, server.URL+"/admin/login?next=%2Fsettings", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin login form status=%d", response.StatusCode)
	}
	adminLoginCSRF := cookieValue(t, jar, server.URL, preauthCookie)
	response = request(t, client, http.MethodPost, server.URL+"/admin/login", url.Values{
		"csrf": {adminLoginCSRF}, "admin_password": {"admin-password-one"}, "next": {"/settings"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("admin login status=%d", response.StatusCode)
	}
	settingsBody, settingsStatus = dashboardGET(t, client, server.URL+"/settings")
	if settingsStatus != http.StatusOK || !strings.Contains(settingsBody, canonicalRoot) {
		t.Fatalf("confirmed administrator could not see storage path: status=%d", settingsStatus)
	}
	// The path appears only on Settings, not in the header of other pages.
	if dashboardBody, dashboardStatus := dashboardGET(t, client, server.URL+"/"); dashboardStatus != http.StatusOK || strings.Contains(dashboardBody, canonicalRoot) {
		t.Fatalf("administrator dashboard showed the storage path: status=%d", dashboardStatus)
	}
	response = request(t, client, http.MethodPost, server.URL+"/repositories", url.Values{
		"csrf": {generalCSRF}, "name": {"Project-One"}, "description": {"actual data"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("create repository status=%d", response.StatusCode)
	}
	if _, exists, err := store.Repository(context.Background(), "project-one"); err != nil || !exists {
		t.Fatalf("repository record exists=%v err=%v", exists, err)
	}

	response = request(t, client, http.MethodPost, server.URL+"/settings", url.Values{
		"csrf": {generalCSRF}, "action": {webui.ActionEnableAccessPassword},
		"admin_password": {"admin-password-one"}, "access_password": {"shared-password-one"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("enable shared password status=%d", response.StatusCode)
	}
	settings, _ = store.Settings(context.Background())
	if settings.AccessMode != "password" {
		t.Fatalf("access mode=%q, want password", settings.AccessMode)
	}
	unauthenticatedGit := httptest.NewRequest(http.MethodGet, "http://localhost/git/project.git/info/refs?service=git-upload-pack", nil)
	if app.AuthorizeGit(unauthenticatedGit) {
		t.Fatal("protected mode authorized Git without Basic credentials")
	}
	wrongGit := httptest.NewRequest(http.MethodGet, "http://localhost/git/project.git/info/refs?service=git-upload-pack", nil)
	wrongGit.SetBasicAuth("owngit", "wrong-password")
	if app.AuthorizeGit(wrongGit) {
		t.Fatal("protected mode authorized an incorrect shared password")
	}
	validGit := httptest.NewRequest(http.MethodGet, "http://localhost/git/project.git/info/refs?service=git-upload-pack", nil)
	validGit.SetBasicAuth("owngit", "shared-password-one")
	if !app.AuthorizeGit(validGit) {
		t.Fatal("protected mode rejected the correct shared password")
	}
	gitDiscoveryURL := server.URL + "/git/project-one.git/info/refs?service=git-upload-pack"
	gitRequest, _ := http.NewRequest(http.MethodGet, gitDiscoveryURL, nil)
	gitResponse, err := client.Do(gitRequest)
	noErr(t, err)
	gitResponse.Body.Close()
	if gitResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("protected Git discovery without Basic auth status=%d", gitResponse.StatusCode)
	}
	gitRequest, _ = http.NewRequest(http.MethodGet, gitDiscoveryURL, nil)
	gitRequest.SetBasicAuth("owngit", "shared-password-one")
	gitResponse, err = client.Do(gitRequest)
	noErr(t, err)
	gitResponse.Body.Close()
	if gitResponse.StatusCode != http.StatusOK {
		t.Fatalf("protected Git discovery with shared password status=%d", gitResponse.StatusCode)
	}

	anonymousJar, _ := cookiejar.New(nil)
	anonymous := &http.Client{Jar: anonymousJar, CheckRedirect: client.CheckRedirect}
	response = request(t, anonymous, http.MethodGet, server.URL+"/", nil, "")
	if response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(response.Header.Get("Location"), "/login") {
		t.Fatalf("protected dashboard status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	response = request(t, anonymous, http.MethodGet, server.URL+"/login", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login form status=%d", response.StatusCode)
	}
	loginCSRF := cookieValue(t, anonymousJar, server.URL, preauthCookie)
	response = request(t, anonymous, http.MethodPost, server.URL+"/login", url.Values{
		"csrf": {loginCSRF}, "password": {"shared-password-one"}, "next": {"/"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("shared login status=%d", response.StatusCode)
	}
	loggedInToken := cookieValue(t, anonymousJar, server.URL, generalCookie)
	loggedIn, ok, _ := store.Session(context.Background(), loggedInToken, "general", time.Now())
	if !ok {
		t.Fatal("shared login did not create a session")
	}
	response = request(t, anonymous, http.MethodPost, server.URL+"/settings", url.Values{
		"csrf": {loggedIn.CSRF}, "action": {webui.ActionChangeAdminPassword},
		"admin_password": {"admin-password-one"}, "new_admin_password": {"admin-password-two"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("change admin password status=%d", response.StatusCode)
	}
	adminHash, _ := store.PasswordHash(context.Background(), "admin")
	if auth.CheckPassword(adminHash, "admin-password-one") || !auth.CheckPassword(adminHash, "admin-password-two") {
		t.Fatal("administrator password transition was not persisted")
	}
	response = request(t, anonymous, http.MethodPost, server.URL+"/settings", url.Values{
		"csrf": {loggedIn.CSRF}, "action": {webui.ActionDisableAccessPassword},
		"admin_password": {"admin-password-two"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("disable shared password status=%d", response.StatusCode)
	}
	settings, _ = store.Settings(context.Background())
	if settings.AccessMode != "open" {
		t.Fatalf("access mode=%q, want open", settings.AccessMode)
	}
	if !app.AuthorizeGit(unauthenticatedGit) {
		t.Fatal("Git remained protected after shared password was disabled")
	}
	gitRequest, _ = http.NewRequest(http.MethodGet, gitDiscoveryURL, nil)
	gitResponse, err = client.Do(gitRequest)
	noErr(t, err)
	gitResponse.Body.Close()
	if gitResponse.StatusCode != http.StatusOK {
		t.Fatalf("open Git discovery after disabling password status=%d", gitResponse.StatusCode)
	}
}

func TestSetupGETAndHEADDoNotConsumeCapabilityAndFormNeedsOrigin(t *testing.T) {
	app, store, _ := newTestApp(t)
	noErr(t, store.PutBootstrap(context.Background(), "owner-token", time.Now().Add(time.Hour)))
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodGet} {
		response := request(t, client, method, server.URL+"/setup", nil, "")
		if method == http.MethodGet && response.StatusCode != http.StatusOK {
			t.Fatalf("%s setup status=%d", method, response.StatusCode)
		}
	}
	csrf := cookieValue(t, jar, server.URL, preauthCookie)
	response := request(t, client, http.MethodPost, server.URL+"/setup/redeem", url.Values{"csrf": {csrf}, "token": {"owner-token"}}, "")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("redeem without Origin status=%d, want 403", response.StatusCode)
	}
	response = request(t, client, http.MethodPost, server.URL+"/setup/redeem", url.Values{"csrf": {csrf}, "token": {"owner-token"}}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("capability was consumed by GET/HEAD or rejected after CSRF retry: status=%d", response.StatusCode)
	}
}

func newTestApp(t *testing.T) (*App, *state.Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := state.Open(context.Background(), filepath.Join(root, "state"))
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	repositoryRoot := filepath.Join(root, "repositories")
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks()}
	gitHandler, err := githttp.New(runner, manager, "", 2)
	noErr(t, err)
	authentication := &auth.Manager{Store: store, SessionLife: time.Hour, AdminSessionLife: 5 * time.Minute}
	renderer, err := webui.New()
	noErr(t, err)
	app := &App{
		Store: store, Auth: authentication, Repositories: manager, GitHTTP: gitHandler,
		Renderer: renderer, Hosts: NewHostPolicy(), SuggestedRepositoryRoot: repositoryRoot,
		GitVersion: "git version test", HTTPBackendFound: true,
	}
	gitHandler.Authorize = app.AuthorizeGit
	// Background activity counting ends before the store and directories go.
	t.Cleanup(app.StopBackground)
	return app, store, repositoryRoot
}

// newConfiguredApp returns an app whose setup is complete in open mode with
// the administrator password "admin-password" and no repositories yet.
func newConfiguredApp(t *testing.T) *App {
	t.Helper()
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, err := filepath.EvalSymlinks(repositoryRoot)
	noErr(t, err)
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, true))
	app.Repositories.SetRoot(canonical)
	return app
}

// serve starts a test server for handler and closes it when the test ends.
func serve(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func request(t *testing.T, client *http.Client, method, target string, values url.Values, origin string) *http.Response {
	t.Helper()
	var body *strings.Reader
	if values == nil {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(values.Encode())
	}
	request, err := http.NewRequest(method, target, body)
	noErr(t, err)
	if values != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := client.Do(request)
	noErr(t, err)
	response.Body.Close()
	return response
}

func cookieValue(t *testing.T, jar http.CookieJar, rawURL, name string) string {
	t.Helper()
	parsed, _ := url.Parse(rawURL)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	t.Fatalf("cookie %s not found", name)
	return ""
}

// noErr stops the test when err is not nil.
func noErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// noErrf stops the test when err is not nil, naming the failed step.
func noErrf(t testing.TB, err error, format string, args ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", fmt.Sprintf(format, args...), err)
	}
}
