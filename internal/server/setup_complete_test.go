package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Setup that commits but cannot remove the obsolete owner setup files still
// reports the failure, and the running process serves the committed setup:
// the repository root is applied and setup completion work starts.
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
	response = request(t, client, http.MethodPost, server.URL+"/setup", url.Values{
		"csrf": {session.CSRF}, "storage_path": {repositoryRoot}, "access_mode": {"open"},
		"admin_password": {"admin-password-one"}, "insecure_ack": {"on"},
	}, server.URL)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("setup with failed cleanup status=%d", response.StatusCode)
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
