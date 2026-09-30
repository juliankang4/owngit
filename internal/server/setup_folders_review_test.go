package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/webui"
)

func folderStartRequest(t *testing.T, app *App, path string) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{"path": {path}, "start": {"1"}, "csrf": {"owner-csrf"}}
	request := httptest.NewRequest(http.MethodPost, "http://localhost"+setupFoldersPath, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "http://localhost")
	request.AddCookie(&http.Cookie{Name: setupCookie, Value: "owner-session"})
	writer := httptest.NewRecorder()
	app.Handler().ServeHTTP(writer, request)
	return writer
}

func folderResponseFacts(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var result map[string]any
	noErr(t, json.Unmarshal(response.Body.Bytes(), &result))
	return result
}

func TestFolderChooserMissingStartOpensExistingParent(t *testing.T) {
	app, store, _ := newTestApp(t)
	home := t.TempDir()
	app.SuggestedRepositoryRoot = filepath.Join(home, "OwnGit-Repositories")
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	for _, path := range []string{"", app.SuggestedRepositoryRoot, filepath.Join(home, "outer", "inner")} {
		response := folderStartRequest(t, app, path)
		if response.Code != http.StatusOK {
			t.Errorf("start %q: %d %s", path, response.Code, response.Body.String())
			continue
		}
		result := readFolderResult(t, response)
		facts := folderResponseFacts(t, response)
		wantName := "OwnGit-Repositories"
		if strings.HasSuffix(path, "inner") {
			wantName = "outer"
		}
		if result.Path != home || result.Error != "" || facts["started_at_parent"] != true || facts["suggested_name"] != wantName {
			t.Errorf("start %q: %s", path, response.Body.String())
		}
	}
	// Explicit navigation to a removed folder still reports its absence.
	response := folderRequest(t, app, setupFoldersPath, app.SuggestedRepositoryRoot, "", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != http.StatusNotFound || readFolderResult(t, response).Error != webui.MsgFolderMissing {
		t.Errorf("navigation: %d %s", response.Code, response.Body.String())
	}
	response = folderRequest(t, app, setupFolderCreatePath, home, "OwnGit-Repositories", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != http.StatusOK {
		t.Fatalf("create suggestion: %d %s", response.Code, response.Body.String())
	}
	response = folderStartRequest(t, app, "")
	if response.Code != http.StatusOK || readFolderResult(t, response).Path != app.SuggestedRepositoryRoot || folderResponseFacts(t, response)["started_at_parent"] == true {
		t.Fatalf("existing suggestion: %d %s", response.Code, response.Body.String())
	}
}

func TestFolderChooserFileAncestorIsNotDirectory(t *testing.T) {
	app, store, _ := newTestApp(t)
	root := t.TempDir()
	noErr(t, os.WriteFile(filepath.Join(root, "file"), []byte("fixture"), 0o600))
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	for _, path := range []string{filepath.Join(root, "file", "child"), filepath.Join(root, "file", "outer", "inner")} {
		for _, response := range []*httptest.ResponseRecorder{
			folderStartRequest(t, app, path),
			folderRequest(t, app, setupFoldersPath, path, "", "owner-session", "owner-csrf", "localhost", "http://localhost"),
		} {
			if response.Code != http.StatusUnprocessableEntity || readFolderResult(t, response).Error != webui.MsgFolderNotDirectory {
				t.Errorf("file ancestor: %d %s", response.Code, response.Body.String())
			}
		}
	}
}

func TestFolderChooserLinksToFoldersAreListed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("junction coverage is Windows-specific")
	}
	app, store, _ := newTestApp(t)
	root := t.TempDir()
	target := t.TempDir()
	noErr(t, os.Mkdir(filepath.Join(root, "real"), 0o700))
	noErr(t, os.Mkdir(filepath.Join(target, "inside"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(target, "file"), []byte("fixture"), 0o600))
	noErr(t, os.Symlink(target, filepath.Join(root, "linked")))
	noErr(t, os.Symlink(filepath.Join(target, "missing"), filepath.Join(root, "broken")))
	noErr(t, os.Symlink(filepath.Join(target, "file"), filepath.Join(root, "file-link")))
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	response := folderRequest(t, app, setupFoldersPath, root, "", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != http.StatusOK {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	result := readFolderResult(t, response)
	if !reflect.DeepEqual(result.Folders, []folderEntry{{Name: "linked", Path: filepath.Join(root, "linked")}, {Name: "real", Path: filepath.Join(root, "real")}}) {
		t.Fatalf("linked folders=%+v", result.Folders)
	}
	response = folderRequest(t, app, setupFoldersPath, filepath.Join(root, "linked"), "", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != http.StatusOK || len(readFolderResult(t, response).Folders) != 1 {
		t.Fatalf("open link: %d %s", response.Code, response.Body.String())
	}
}

func TestFolderChooserUnsupportedNameDoesNotHideOtherFolders(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires a filesystem accepting non-UTF-8 names; Debian run covers it")
	}
	app, store, _ := newTestApp(t)
	root := t.TempDir()
	noErr(t, os.Mkdir(filepath.Join(root, "visible"), 0o700))
	noErr(t, os.Mkdir(filepath.Join(root, "legacy-\xff"), 0o700))
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	response := folderRequest(t, app, setupFoldersPath, root, "", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != http.StatusOK {
		t.Fatalf("partial listing: %d %s", response.Code, response.Body.String())
	}
	result := readFolderResult(t, response)
	if len(result.Folders) != 1 || result.Folders[0].Name != "visible" || folderResponseFacts(t, response)["skipped_names"] != true {
		t.Fatalf("partial listing=%s", response.Body.String())
	}
}

func TestFolderFixtureUsesTestCleanupWithoutTrash(t *testing.T) {
	sandbox := t.TempDir()
	home, temp := filepath.Join(sandbox, "home"), filepath.Join(sandbox, "temp")
	noErr(t, os.MkdirAll(filepath.Join(home, ".Trash"), 0o700))
	noErr(t, os.Mkdir(temp, 0o700))
	for key, value := range map[string]string{"HOME": home, "USERPROFILE": home, "TMPDIR": temp, "TMP": temp, "TEMP": temp} {
		t.Setenv(key, value)
	}
	t.Run("fixture lifetime", func(t *testing.T) { noErr(t, os.Mkdir(filepath.Join(folderFixture(t), "child"), 0o700)) })
	for _, path := range []string{temp, filepath.Join(home, ".Trash")} {
		entries, err := os.ReadDir(path)
		noErr(t, err)
		if len(entries) != 0 {
			t.Errorf("fixture left entries in %s: %v", path, entries)
		}
	}
}

func TestFolderChooserOpenedParentCannotRedirectCreation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows directory handles prevent rename; junction behavior is covered separately")
	}
	app, store, _ := newTestApp(t)
	base := t.TempDir()
	selected, moved, other := filepath.Join(base, "selected"), filepath.Join(base, "moved"), filepath.Join(base, "other")
	noErr(t, os.Mkdir(selected, 0o700))
	noErr(t, os.Mkdir(other, 0o700))
	parent, err := os.OpenRoot(selected)
	noErr(t, err)
	defer parent.Close()
	noErr(t, os.Rename(selected, moved))
	noErr(t, os.Symlink(other, selected))
	_, err = app.createFolder(context.Background(), parent, "made")
	noErr(t, err)
	if _, err := os.Stat(filepath.Join(moved, "made")); err != nil {
		t.Errorf("opened parent did not receive folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "made")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("creation redirected to replacement: %v", err)
	}
	// The setup recheck remains adjacent to the write, even for an already opened parent.
	noErr(t, store.CompleteSetup(context.Background(), other, "open", "", fixturePasswordHash(t, "admin-password"), true))
	_, err = app.createFolder(context.Background(), parent, "after-setup")
	if !errors.Is(err, errFolderSetupDone) {
		t.Errorf("setup recheck: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moved, "after-setup")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("write after setup")
	}
}

func TestFolderChooserSelectedSymlinkParentCreatesInTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("junction coverage is Windows-specific")
	}
	app, _, _ := newTestApp(t)
	base := t.TempDir()
	target, link := filepath.Join(base, "target"), filepath.Join(base, "linked")
	noErr(t, os.Mkdir(target, 0o700))
	noErr(t, os.Symlink(target, link))
	parent, err := os.OpenRoot(link)
	noErr(t, err)
	defer parent.Close()
	result, err := app.createFolder(context.Background(), parent, "made")
	noErr(t, err)
	if result.Path != filepath.Join(link, "made") {
		t.Errorf("link path not preserved: %+v", result)
	}
	info, err := os.Stat(filepath.Join(target, "made"))
	noErr(t, err)
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("created mode=%v", info.Mode())
	}
}

func TestFolderChooserControlNamesAreRefused(t *testing.T) {
	app, store, _ := newTestApp(t)
	root := t.TempDir()
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	for _, name := range []string{"tab\tname", "line\nname", "carriage\rname", "control\x01name", "delete\x7fname"} {
		response := folderRequest(t, app, setupFolderCreatePath, root, name, "owner-session", "owner-csrf", "localhost", "http://localhost")
		if response.Code != http.StatusBadRequest || readFolderResult(t, response).Error != webui.MsgFolderInvalidName {
			t.Errorf("control name: %d %s", response.Code, response.Body.String())
		}
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("control name created: %q (%v)", name, err)
		}
	}
}
