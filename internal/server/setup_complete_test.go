package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Setup that commits but cannot remove the used owner setup files is
// answered as finished with its cause logged, and the running process serves
// the committed setup: the repository root is applied and setup completion
// work starts. TestSetupFinishedNoticeIsShownOnceAfterSetup checks the
// notice that the file remains.
func TestCommittedSetupStartsWorkWhenSetupFileCleanupFails(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	started := 0
	app.OnSetupComplete = func() { started++ }
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	// A nonempty directory at the owner file path makes its removal fail.
	blocked := filepath.Join(store.Dir(), "owner-setup.html")
	noErr(t, os.MkdirAll(filepath.Join(blocked, "keep"), 0o700))
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	request(t, client, http.MethodGet, server.URL+"/setup", nil, "")
	response := request(t, client, http.MethodPost, server.URL+"/setup/redeem", url.Values{
		"csrf": {cookieValue(t, jar, server.URL, preauthCookie)}, "token": {"synthetic-owner-token"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeem status=%d", response.StatusCode)
	}
	session, ok, err := store.Session(context.Background(), cookieValue(t, jar, server.URL, setupCookie), "setup", time.Now())
	if err != nil || !ok {
		t.Fatalf("setup session ok=%v err=%v", ok, err)
	}
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	serverLog := captureServerLog(t)
	response = request(t, client, http.MethodPost, server.URL+"/setup", url.Values{
		"csrf": {session.CSRF}, "storage_path": {repositoryRoot}, "access_mode": {"open"},
		"admin_password": {"admin-password-one"}, "insecure_ack": {"on"},
	}, server.URL)
	if location := response.Header.Get("Location"); response.StatusCode != http.StatusSeeOther || location != "/?notice=setup_file_remains" {
		t.Fatalf("setup with failed cleanup status=%d location=%q", response.StatusCode, location)
	}
	checkLoggedSteps(t, "setup file removal", loggedFailures(serverLog, 0), "setup file removal")
	if !strings.Contains(serverLog.String(), "owner-setup.html") {
		t.Fatalf("log does not name the file: %s", serverLog)
	}
	settings, err := store.Settings(context.Background())
	if err != nil || !settings.Initialized {
		t.Fatalf("setup was not committed settings=%+v err=%v", settings, err)
	}
	if started != 1 {
		t.Fatalf("setup completion work started %d times", started)
	}
	if app.Repositories.RepositoryRoot() != settings.RepositoryRoot {
		t.Fatalf("process root=%q want %q", app.Repositories.RepositoryRoot(), settings.RepositoryRoot)
	}
}

// Settings that cannot be saved are answered as unavailable with the cause
// logged, never as setup finished by another browser, and nothing is saved.
func TestSetupThatCannotBeSavedIsUnavailable(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	request(t, client, http.MethodGet, server.URL+"/setup", nil, "")
	response := request(t, client, http.MethodPost, server.URL+"/setup/redeem", url.Values{
		"csrf": {cookieValue(t, jar, server.URL, preauthCookie)}, "token": {"synthetic-owner-token"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeem status=%d", response.StatusCode)
	}
	session, ok, err := store.Session(context.Background(), cookieValue(t, jar, server.URL, setupCookie), "setup", time.Now())
	if err != nil || !ok {
		t.Fatalf("setup session ok=%v err=%v", ok, err)
	}
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	refuseWrites(t, store, "refuse_passwords", "INSERT ON passwords")
	serverLog := captureServerLog(t)
	result := browserForm(t, client, server.URL+"/setup", url.Values{
		"csrf": {session.CSRF}, "storage_path": {repositoryRoot}, "access_mode": {"open"},
		"admin_password": {"admin-password-one"}, "insecure_ack": {"on"},
	}, server.URL)
	if result.status != http.StatusServiceUnavailable || strings.Contains(result.body, "Another browser finished setup first.") {
		t.Fatalf("setup that could not be saved status=%d body=%s", result.status, result.body)
	}
	checkLoggedSteps(t, "setup save", loggedFailures(serverLog, 0), "setup completion")
	if !strings.Contains(serverLog.String(), "injected failure") {
		t.Fatalf("log does not name the cause: %s", serverLog)
	}
	if settings, err := store.Settings(context.Background()); err != nil || settings.Initialized {
		t.Fatalf("settings=%+v err=%v, want nothing saved", settings, err)
	}
}
