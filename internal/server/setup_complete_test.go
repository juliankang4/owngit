package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/webui"
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

func setupAnswers(root string) SetupAnswers {
	return SetupAnswers{StoragePath: root, AccessMode: "open", AdminPassword: "admin-password-one", InsecureAccepted: true}
}

func activeSetupRoot(t *testing.T) (*App, string) {
	t.Helper()
	first, _, root := newTestApp(t)
	feedback, err := first.CompleteSetup(context.Background(), setupAnswers(root), true)
	if err != nil || len(feedback.Problems) != 0 || len(feedback.Warnings) != 0 {
		t.Fatalf("first setup feedback=%+v err=%v", feedback, err)
	}
	noErr(t, first.Repositories.ClaimStorage())
	t.Cleanup(first.Repositories.ReleaseStorage)
	_, err = first.Repositories.Create(context.Background(), "owned", "")
	noErr(t, err)
	return first, root
}

// Terminal and browser both use CompleteSetup. The refusal must arrive before
// any initialized state or success notice, including another spelling of root.
func TestSetupRefusesActiveStorageAndCanChooseAnotherFolder(t *testing.T) {
	for _, spelling := range []string{"original", "resolved", "symlink"} {
		t.Run(spelling, func(t *testing.T) {
			first, root := activeSetupRoot(t)
			chosen := root
			if spelling == "resolved" {
				var err error
				chosen, err = filepath.EvalSymlinks(root)
				noErr(t, err)
			} else if spelling == "symlink" {
				chosen = filepath.Join(t.TempDir(), "root-alias")
				if err := os.Symlink(root, chosen); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			second, store, alternative := newTestApp(t)
			t.Cleanup(second.Repositories.ReleaseStorage)
			completed := 0
			second.OnSetupComplete = func() { completed++ }
			feedback, err := second.CompleteSetup(context.Background(), setupAnswers(chosen), true)
			if err != nil || len(feedback.Problems) != 1 || feedback.Problems[0].Code != webui.MsgSetupStorageInUse || len(feedback.Warnings) != 0 {
				t.Fatalf("busy setup feedback=%+v err=%v", feedback, err)
			}
			settings, err := store.Settings(context.Background())
			noErr(t, err)
			if settings.Initialized || completed != 0 || second.setupResult.Load() != nil || second.Repositories.RepositoryRoot() != "" {
				t.Fatalf("busy storage announced success: settings=%+v completed=%d", settings, completed)
			}
			feedback, err = second.CompleteSetup(context.Background(), setupAnswers(alternative), true)
			if err != nil || len(feedback.Problems) != 0 || len(feedback.Warnings) != 0 || completed != 1 {
				t.Fatalf("alternative setup feedback=%+v err=%v completed=%d", feedback, err, completed)
			}
			_, err = second.Repositories.Create(context.Background(), "separate", "")
			noErr(t, err)
			if _, found, err := first.Store.Repository(context.Background(), "owned"); err != nil || !found {
				t.Fatalf("first owner's repository found=%v err=%v", found, err)
			}
		})
	}
}

func TestSetupHoldsClaimThroughCommitAndReleasesFailedChoice(t *testing.T) {
	ctx := context.Background()
	app, store, root := newTestApp(t)
	t.Cleanup(app.Repositories.ReleaseStorage)
	noErr(t, os.Mkdir(root, 0o700))
	noErr(t, os.WriteFile(filepath.Join(root, "existing-data"), []byte("preserve"), 0o600))
	noErr(t, store.Exec(ctx, `CREATE TRIGGER refuse_setup BEFORE INSERT ON passwords BEGIN SELECT RAISE(ABORT,'setup record refused'); END`))
	if feedback, err := app.CompleteSetup(ctx, setupAnswers(root), true); !errors.Is(err, ErrSetupUnavailable) || len(feedback.Problems) != 0 || len(feedback.Warnings) != 0 {
		t.Fatalf("failed save feedback=%+v err=%v", feedback, err)
	}
	probe := &repository.Manager{Root: root}
	if err := probe.ClaimStorage(); err != nil {
		t.Fatalf("tentative claim leaked after failed save: %v", err)
	}
	probe.ReleaseStorage()
	if app.Repositories.RepositoryRoot() != "" {
		t.Fatal("failed save selected its root")
	}
	noErr(t, store.Exec(ctx, `DROP TRIGGER refuse_setup`))
	app.OnSetupComplete = func() {
		if err := probe.ClaimStorage(); !errors.Is(err, repository.ErrStorageInUse) {
			probe.ReleaseStorage()
			t.Errorf("claim not held when setup announces completion: %v", err)
		}
	}
	if feedback, err := app.CompleteSetup(ctx, setupAnswers(root), true); err != nil || len(feedback.Problems) != 0 || len(feedback.Warnings) != 0 {
		t.Fatalf("recovered setup feedback=%+v err=%v", feedback, err)
	}
	// A competing finish must not disturb the winning claim or root.
	if _, err := app.CompleteSetup(ctx, setupAnswers(filepath.Join(t.TempDir(), "other")), true); !errors.Is(err, ErrSetupCompletedElsewhere) {
		t.Fatalf("competing setup err=%v", err)
	}
	if err := probe.ClaimStorage(); !errors.Is(err, repository.ErrStorageInUse) {
		probe.ReleaseStorage()
		t.Fatalf("competing setup lost the winner's claim: %v", err)
	}
}

func TestBrowserSetupRefusesActiveFolderAndRetainsTheForm(t *testing.T) {
	_, root := activeSetupRoot(t)
	app, store, alternative := newTestApp(t)
	t.Cleanup(app.Repositories.ReleaseStorage)
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	request(t, client, http.MethodGet, server.URL+"/setup", nil, "")
	request(t, client, http.MethodPost, server.URL+"/setup/redeem", url.Values{
		"csrf": {cookieValue(t, jar, server.URL, preauthCookie)}, "token": {"synthetic-owner-token"},
	}, server.URL)
	session, found, err := store.Session(context.Background(), cookieValue(t, jar, server.URL, setupCookie), "setup", time.Now())
	if err != nil || !found {
		t.Fatalf("session found=%v err=%v", found, err)
	}
	values := url.Values{"csrf": {session.CSRF}, "storage_path": {root}, "access_mode": {"open"}, "admin_password": {"admin-password-one"}, "insecure_ack": {"on"}}
	result := browserForm(t, client, server.URL+"/setup", values, server.URL)
	if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, "Another OwnGit server is using that folder.") || strings.Contains(result.body, "Setup finished.") {
		t.Fatalf("busy browser setup status=%d body=%s", result.status, result.body)
	}
	if !strings.Contains(result.body, `name="storage_path"`) || !strings.Contains(result.body, root) {
		t.Fatal("refusal did not retain the editable storage choice")
	}
	values.Set("storage_path", alternative)
	response := request(t, client, http.MethodPost, server.URL+"/setup", values, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("alternate browser setup status=%d", response.StatusCode)
	}
}
