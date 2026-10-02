//go:build !windows

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

	"owngit/internal/webui"
)

func makeSetupFixtureReachable(t *testing.T, path string) {
	t.Helper()
	temporary := filepath.Clean(os.TempDir())
	relative, err := filepath.Rel(temporary, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		t.Fatalf("%s is not below temporary folder %s", path, temporary)
	}
	for parent := filepath.Dir(path); parent != temporary; parent = filepath.Dir(parent) {
		noErr(t, os.Chmod(parent, 0o755))
	}
}

func TestSetupWarnsForSharedRepositoryFolderAndContinues(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	app, store, root := newTestApp(t)
	makeSetupFixtureReachable(t, root)
	noErr(t, os.Mkdir(root, 0o700))
	noErr(t, os.Chmod(root, 0o777))

	checked := app.CheckRepositoryFolder(root)
	canonical, err := filepath.EvalSymlinks(root)
	noErr(t, err)
	if len(checked.Problems) != 0 || len(checked.Warnings) != 1 || checked.Warnings[0].Code != webui.MsgSetupStorageShared || checked.Warnings[0].Detail != canonical {
		t.Fatalf("folder check=%+v", checked)
	}
	feedback, err := app.CompleteSetup(context.Background(), setupAnswers(root), true)
	if err != nil || len(feedback.Problems) != 0 || len(feedback.Warnings) != 1 || feedback.Warnings[0].Code != webui.MsgSetupStorageShared {
		t.Fatalf("setup feedback=%+v err=%v", feedback, err)
	}
	settings, err := store.Settings(context.Background())
	noErr(t, err)
	if !settings.Initialized {
		t.Fatal("the storage warning blocked setup")
	}
}

func TestSetupDoesNotWarnForSharedFolderBelowPrivateParent(t *testing.T) {
	app, _, root := newTestApp(t)
	noErr(t, os.Mkdir(root, 0o777))
	noErr(t, os.Chmod(filepath.Dir(root), 0o700))
	noErr(t, os.Chmod(root, 0o777))

	checked := app.CheckRepositoryFolder(root)
	if len(checked.Problems) != 0 || len(checked.Warnings) != 0 {
		t.Fatalf("folder below private parent check=%+v", checked)
	}
}

func TestSetupPrivateRepositoryFolderHasNoWarning(t *testing.T) {
	app, _, root := newTestApp(t)
	noErr(t, os.Mkdir(root, 0o700))
	noErr(t, os.Chmod(root, 0o700))
	checked := app.CheckRepositoryFolder(root)
	if len(checked.Problems) != 0 || len(checked.Warnings) != 0 {
		t.Fatalf("private folder check=%+v", checked)
	}
}

func TestBrowserSetupShowsSharedFolderWarningOnlyToItsBrowser(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	app, store, root := newTestApp(t)
	makeSetupFixtureReachable(t, root)
	noErr(t, os.Mkdir(root, 0o700))
	noErr(t, os.Chmod(root, 0o777))
	noErr(t, store.PutBootstrap(context.Background(), "synthetic-owner-token", time.Now().Add(time.Hour)))
	server := serve(t, app.Handler())
	owner := newSetupBrowser(t, server)
	owner.get("/setup")
	owner.post("/setup/redeem", url.Values{"csrf": {owner.cookie(preauthCookie)}, "token": {"synthetic-owner-token"}})
	session, ok, err := store.Session(context.Background(), owner.cookie(setupCookie), "setup", time.Now())
	if err != nil || !ok {
		t.Fatalf("setup session ok=%v err=%v", ok, err)
	}
	status, location, body := owner.post("/setup", url.Values{
		"csrf": {session.CSRF}, "storage_path": {root}, "access_mode": {"open"},
		"admin_password": {"admin-password-one"}, "insecure_ack": {"1"},
	})
	if status != http.StatusSeeOther || location != "/?notice=setup_completed_storage_warning" {
		t.Fatalf("setup status=%d location=%q body=%s", status, location, body)
	}
	_, _, body = owner.get(location)
	if !strings.Contains(body, webui.Text(webui.LangEN, webui.MsgSetupStorageShared)) {
		t.Fatalf("owner did not see the storage warning: %s", body)
	}
	stranger := newSetupBrowser(t, server)
	_, _, publicBody := stranger.get(location)
	if strings.Contains(publicBody, webui.Text(webui.LangEN, webui.MsgSetupStorageShared)) || strings.Contains(publicBody, filepath.Clean(root)) {
		t.Fatalf("a browser without the result cookie saw the warning or path: %s", publicBody)
	}
}
