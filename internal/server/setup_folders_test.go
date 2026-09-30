package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func folderFixture(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func folderRequest(t *testing.T, app *App, route, path, name, token, csrf, host, origin string) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{"path": {path}, "name": {name}, "csrf": {csrf}}
	request := httptest.NewRequest(http.MethodPost, "http://"+host+route, strings.NewReader(values.Encode()))
	request.RemoteAddr = "127.0.0.1:54000"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", origin)
	if token != "" {
		request.AddCookie(&http.Cookie{Name: setupCookie, Value: token})
	}
	writer := httptest.NewRecorder()
	app.Handler().ServeHTTP(writer, request)
	return writer
}

func readFolderResult(t *testing.T, response *httptest.ResponseRecorder) folderResult {
	t.Helper()
	var result folderResult
	noErr(t, json.Unmarshal(response.Body.Bytes(), &result))
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("folder response may be cached")
	}
	return result
}

func TestSetupFoldersRequireOwner(t *testing.T) {
	app, store, _ := newTestApp(t)
	root := folderFixture(t)
	noErr(t, os.Mkdir(filepath.Join(root, "private-child"), 0o700))
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	noErr(t, store.CreateSession(context.Background(), "admin-session", "admin", "owner-csrf", 1, time.Now().Add(time.Hour)))
	for _, test := range []struct {
		label, token, csrf, host, origin string
		status                           int
	}{
		{"no session", "", "owner-csrf", "localhost", "http://localhost", 403},
		{"expired session", "expired-session", "owner-csrf", "localhost", "http://localhost", 403},
		{"admin is not setup", "admin-session", "owner-csrf", "localhost", "http://localhost", 403},
		{"no csrf", "owner-session", "", "localhost", "http://localhost", 403},
		{"wrong csrf", "owner-session", "other", "localhost", "http://localhost", 403},
		{"no origin", "owner-session", "owner-csrf", "localhost", "", 403},
		{"wrong origin", "owner-session", "owner-csrf", "localhost", "http://other.test", 403},
		{"wrong origin port", "owner-session", "owner-csrf", "localhost", "http://localhost:9000", 403},
		{"unbound host", "owner-session", "owner-csrf", "other.test", "http://other.test", 421},
	} {
		t.Run(test.label, func(t *testing.T) {
			if test.label == "expired session" {
				noErr(t, store.StartApprovedSetupSession(context.Background(), "expired-session", "owner-csrf", time.Now().Add(-time.Hour)))
				t.Cleanup(func() {
					noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
				})
			}
			for _, route := range []string{setupFoldersPath, setupFolderCreatePath} {
				response := folderRequest(t, app, route, root, "unauthorized", test.token, test.csrf, test.host, test.origin)
				if response.Code != test.status || strings.Contains(response.Body.String(), "private-child") || strings.Contains(response.Body.String(), root) {
					t.Fatalf("%s status=%d body=%s", route, response.Code, response.Body.String())
				}
				if _, err := os.Stat(filepath.Join(root, "unauthorized")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("unauthorized create: %v", err)
				}
				if app.folderBusy.Load() {
					t.Fatal("unauthorized request started filesystem work")
				}
			}
		})
	}
	app.setupHosts.set("owner-session", "other.test")
	response := folderRequest(t, app, setupFoldersPath, root, "", "owner-session", "owner-csrf", "other.test", "http://other.test")
	if response.Code != http.StatusOK {
		t.Fatalf("bound host: %d %s", response.Code, response.Body.String())
	}
	result := readFolderResult(t, response)
	if len(result.Folders) != 1 || result.Folders[0].Name != "private-child" {
		t.Fatalf("listing=%+v", result)
	}
	noErr(t, store.CompleteSetup(context.Background(), root, "open", "", fixturePasswordHash(t, "admin-password"), true))
	for _, host := range []string{"localhost", "other.test"} {
		for _, route := range []string{setupFoldersPath, setupFolderCreatePath} {
			response := folderRequest(t, app, route, root, "after-setup", "owner-session", "owner-csrf", host, "http://"+host)
			want := http.StatusConflict
			if host == "other.test" {
				want = http.StatusMisdirectedRequest
			}
			if response.Code != want || strings.Contains(response.Body.String(), "private-child") {
				t.Fatalf("after setup %s %s: %d %s", host, route, response.Code, response.Body.String())
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "after-setup")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("folder created after setup")
	}
}

func TestSetupFoldersListAndCreate(t *testing.T) {
	app, store, _ := newTestApp(t)
	root := folderFixture(t)
	app.SuggestedRepositoryRoot = root
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	for _, name := range []string{"zeta", "Alpha", "한글", ".hidden"} {
		noErr(t, os.Mkdir(filepath.Join(root, name), 0o700))
	}
	noErr(t, os.WriteFile(filepath.Join(root, "not-a-folder"), []byte("fixture"), 0o600))
	for _, path := range []string{root, ""} {
		response := folderRequest(t, app, setupFoldersPath, path, "", "owner-session", "owner-csrf", "localhost", "http://localhost")
		if response.Code != http.StatusOK {
			t.Fatalf("list: %d %s", response.Code, response.Body.String())
		}
		result := readFolderResult(t, response)
		var names []string
		for _, folder := range result.Folders {
			names = append(names, folder.Name)
			if folder.Path != filepath.Join(root, folder.Name) {
				t.Fatalf("folder=%+v", folder)
			}
		}
		if !reflect.DeepEqual(names, []string{"Alpha", "zeta", "한글"}) || result.Path != root || result.Parent != filepath.Dir(root) {
			t.Fatalf("listing=%+v", result)
		}
	}
	app.SuggestedRepositoryRoot = ""
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", root)
	} else {
		t.Setenv("HOME", root)
	}
	homeResponse := folderRequest(t, app, setupFoldersPath, "", "", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if homeResponse.Code != http.StatusOK || readFolderResult(t, homeResponse).Path != root {
		t.Fatalf("home fallback: %d %s", homeResponse.Code, homeResponse.Body.String())
	}
	result, err := listFolders(context.Background(), root, true, false)
	noErr(t, err)
	if len(result.Folders) != 4 || result.Folders[0].Name != ".hidden" {
		t.Fatalf("hidden listing=%+v", result)
	}
	response := folderRequest(t, app, setupFolderCreatePath, root, "new folder", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != http.StatusOK {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	created := readFolderResult(t, response).Path
	info, err := os.Stat(created)
	noErr(t, err)
	if !info.IsDir() || created != filepath.Join(root, "new folder") || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o700) {
		t.Fatalf("created=%s mode=%v", created, info.Mode())
	}
	for _, name := range []string{"new folder", "not-a-folder"} {
		response := folderRequest(t, app, setupFolderCreatePath, root, name, "owner-session", "owner-csrf", "localhost", "http://localhost")
		if response.Code != http.StatusConflict || readFolderResult(t, response).Error != webui.MsgFolderExists {
			t.Fatalf("existing entry: %d %s", response.Code, response.Body.String())
		}
	}
	content, err := os.ReadFile(filepath.Join(root, "not-a-folder"))
	noErr(t, err)
	if string(content) != "fixture" {
		t.Fatal("existing file replaced")
	}
	if runtime.GOOS != "windows" {
		noErr(t, os.Symlink(filepath.Join(root, "Alpha"), filepath.Join(root, "link")))
		response := folderRequest(t, app, setupFolderCreatePath, root, "link", "owner-session", "owner-csrf", "localhost", "http://localhost")
		if response.Code != http.StatusConflict {
			t.Fatalf("link replaced: %d", response.Code)
		}
		link, err := os.Readlink(filepath.Join(root, "link"))
		noErr(t, err)
		if link != filepath.Join(root, "Alpha") {
			t.Fatal("link changed")
		}
	}
	for _, name := range []string{"", " ", ".", "..", "a/b", `a\b`, "nul\x00", strings.Repeat("x", 256)} {
		response := folderRequest(t, app, setupFolderCreatePath, root, name, "owner-session", "owner-csrf", "localhost", "http://localhost")
		if response.Code != http.StatusBadRequest || readFolderResult(t, response).Error != webui.MsgFolderInvalidName {
			t.Fatalf("name %q: %d", name, response.Code)
		}
	}
}

func TestSetupFoldersErrorsAndLimits(t *testing.T) {
	app, store, _ := newTestApp(t)
	root := folderFixture(t)
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	noErr(t, os.WriteFile(filepath.Join(root, "file"), []byte("fixture"), 0o600))
	denied := filepath.Join(root, "denied")
	noErr(t, os.Mkdir(denied, 0o000))
	t.Cleanup(func() { _ = os.Chmod(denied, 0o700) })
	for _, test := range []struct {
		path string
		code webui.MessageCode
	}{
		{filepath.Join(root, "missing"), webui.MsgFolderMissing},
		{filepath.Join(root, "file"), webui.MsgFolderNotDirectory},
		{filepath.Join(root, "file", "child"), webui.MsgFolderNotDirectory},
		{"relative", webui.MsgFolderInvalidPath},
		{denied, webui.MsgFolderDenied},
	} {
		if test.path == denied && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
			t.Log("permission fixture requires non-root POSIX account")
			continue
		}
		response := folderRequest(t, app, setupFoldersPath, test.path, "", "owner-session", "owner-csrf", "localhost", "http://localhost")
		result := readFolderResult(t, response)
		if response.Code == http.StatusOK || result.Error != test.code || result.Folders != nil {
			t.Fatalf("%s: %d %+v", test.path, response.Code, result)
		}
	}
	empty, err := listFolders(context.Background(), root, false, false)
	noErr(t, err)
	if len(empty.Folders) != 1 {
		t.Fatalf("fixture list=%+v", empty)
	}
	large := filepath.Join(root, "large")
	noErr(t, os.Mkdir(large, 0o700))
	for i := 0; i < folderEntryLimit+1; i++ {
		noErr(t, os.Mkdir(filepath.Join(large, fmt.Sprintf("folder-%04d", i)), 0o700))
	}
	result, err := listFolders(context.Background(), large, false, false)
	noErr(t, err)
	if !result.Truncated || len(result.Folders) != folderEntryLimit {
		t.Fatalf("bounded listing=%+v", result)
	}
	for i := 1; i < len(result.Folders); i++ {
		if result.Folders[i-1].Name >= result.Folders[i].Name {
			t.Fatal("listing not sorted")
		}
	}
	result, err = listFolders(context.Background(), filepath.Join(large, "folder-0000"), false, false)
	noErr(t, err)
	if result.Truncated || result.Folders == nil || len(result.Folders) != 0 {
		t.Fatalf("empty listing=%+v", result)
	}
}

func TestSetupFoldersUnavailableAnswersLogTheirCause(t *testing.T) {
	app, store, _ := newTestApp(t)
	root := folderFixture(t)
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	serverLog := captureServerLog(t)
	app.folderBusy.Store(true)
	response := folderRequest(t, app, setupFoldersPath, root, "", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != http.StatusServiceUnavailable || readFolderResult(t, response).Error != webui.MsgFolderBusy {
		t.Fatalf("busy: %d %s", response.Code, response.Body.String())
	}
	app.folderBusy.Store(false)
	endFailureWindows()
	damageSession(t, store, "owner-session", "setup")
	for _, route := range []string{setupFoldersPath, setupFolderCreatePath} {
		response := folderRequest(t, app, route, root, "not-created", "owner-session", "owner-csrf", "localhost", "http://localhost")
		if response.Code != http.StatusServiceUnavailable || readFolderResult(t, response).Error != webui.MsgErrUnavailable {
			t.Fatalf("session read: %d %s", response.Code, response.Body.String())
		}
		endFailureWindows()
	}
	if lines := loggedFailures(serverLog, 0); len(lines) != 3 {
		t.Fatalf("logged %d causes, want 3: %s", len(lines), strings.Join(lines, "\n"))
	}
	if _, err := os.Stat(filepath.Join(root, "not-created")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("unavailable authorization created a folder: %v", err)
	}
}

func TestFolderOperationDeadlineKeepsBlockedWorkBounded(t *testing.T) {
	app := &App{}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	defer close(release)
	before := time.Now()
	_, err := app.folderOperation(ctx, func(context.Context) (folderResult, error) {
		close(started)
		<-release
		defer close(finished)
		return folderResult{}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(before) > time.Second {
		t.Fatalf("deadline err=%v duration=%s", err, time.Since(before))
	}
	<-started
	if !app.folderBusy.Load() {
		t.Fatal("deadline claimed to stop blocked operation")
	}
	_, err = app.folderOperation(context.Background(), func(context.Context) (folderResult, error) {
		t.Error("second worker started")
		return folderResult{}, nil
	})
	if !errors.Is(err, errFolderBusy) {
		t.Fatalf("second operation=%v", err)
	}
	for _, create := range []bool{false, true} {
		status, code := folderProblem(httptest.NewRequest(http.MethodPost, setupFoldersPath, nil), context.DeadlineExceeded, create)
		want := webui.MsgFolderTimeout
		if create {
			want = webui.MsgFolderCreateUnconfirmed
		}
		if status != http.StatusGatewayTimeout || code != want {
			t.Fatalf("timeout: %d %s", status, code)
		}
	}
	// The simulated OS operation is deliberately blocked beyond the response.
	// Cleanup releases it; this test does not claim an actual mount was tested.
	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("fixture worker did not finish")
		}
	})
}

func folderStartRequest(t *testing.T, app *App, path string) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{"path": {path}, "start": {"1"}, "csrf": {"owner-csrf"}}
	request := httptest.NewRequest(http.MethodPost, "http://localhost"+setupFoldersPath, strings.NewReader(values.Encode()))
	request.RemoteAddr = "127.0.0.1:54000"
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
	root, target := t.TempDir(), t.TempDir()
	noErr(t, os.Mkdir(filepath.Join(root, "real"), 0o700))
	noErr(t, os.Mkdir(filepath.Join(target, "inside"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(target, "file"), []byte("fixture"), 0o600))
	noErr(t, os.Symlink(target, filepath.Join(root, "linked")))
	noErr(t, os.Symlink(filepath.Join(target, "missing"), filepath.Join(root, "broken")))
	noErr(t, os.Symlink(filepath.Join(target, "file"), filepath.Join(root, "file-link")))
	noErr(t, os.Symlink("cycle", filepath.Join(root, "cycle")))
	noErr(t, os.Symlink(filepath.Join(target, "file", "child"), filepath.Join(root, "broken-through-file")))
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
	if len(result.Folders) != 1 || result.Folders[0].Name != "visible" || folderResponseFacts(t, response)["skipped_folders"] != true {
		t.Fatalf("partial listing=%s", response.Body.String())
	}
	onlyUnsupported := t.TempDir()
	noErr(t, os.Mkdir(filepath.Join(onlyUnsupported, "legacy-\xff"), 0o700))
	partial, err := listFolders(context.Background(), onlyUnsupported, false, false)
	noErr(t, err)
	if len(partial.Folders) != 0 || !partial.SkippedFolders {
		t.Errorf("all unsupported names: %+v", partial)
	}
	hiddenRoot := t.TempDir()
	noErr(t, os.Mkdir(filepath.Join(hiddenRoot, ".legacy-\xff"), 0o700))
	for _, showHidden := range []bool{false, true} {
		partial, err := listFolders(context.Background(), hiddenRoot, showHidden, false)
		noErr(t, err)
		if partial.SkippedFolders != showHidden {
			t.Errorf("hidden=%v skipped=%v", showHidden, partial.SkippedFolders)
		}
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
		entries, err := os.ReadDir(root)
		noErr(t, err)
		if len(entries) != 0 {
			t.Errorf("invalid-name request created entries: %v", entries)
		}
	}
}

func TestFolderChooserUnreadableLinkKeepsOtherFolders(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires non-root POSIX permission enforcement")
	}
	app, store, _ := newTestApp(t)
	locked := filepath.Join(t.TempDir(), "locked")
	noErr(t, os.MkdirAll(filepath.Join(locked, "inner"), 0o700))
	noErr(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if _, err := os.Stat(filepath.Join(locked, "inner")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("permission fixture did not deny access: %v", err)
	}
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	for _, name := range []string{"into-locked", ".into-locked"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			noErr(t, os.Mkdir(filepath.Join(root, "visible"), 0o700))
			noErr(t, os.Symlink(filepath.Join(locked, "inner"), filepath.Join(root, name)))
			app.SuggestedRepositoryRoot = filepath.Join(root, "OwnGit-Repositories")
			for _, showHidden := range []bool{false, true} {
				values := url.Values{"path": {root}, "csrf": {"owner-csrf"}}
				if showHidden {
					values.Set("hidden", "1")
				}
				request := httptest.NewRequest(http.MethodPost, "http://localhost"+setupFoldersPath, strings.NewReader(values.Encode()))
				request.RemoteAddr = "127.0.0.1:54000"
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				request.Header.Set("Origin", "http://localhost")
				request.AddCookie(&http.Cookie{Name: setupCookie, Value: "owner-session"})
				response := httptest.NewRecorder()
				app.Handler().ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					t.Errorf("%s hidden=%v: %d %s", name, showHidden, response.Code, response.Body.String())
					continue
				}
				result := readFolderResult(t, response)
				var facts map[string]any
				noErr(t, json.Unmarshal(response.Body.Bytes(), &facts))
				wantSkipped := showHidden || !strings.HasPrefix(name, ".")
				if len(result.Folders) != 1 || result.Folders[0].Name != "visible" || (facts["skipped_folders"] == true) != wantSkipped {
					t.Errorf("%s hidden=%v: %s", name, showHidden, response.Body.String())
				}
			}
			response := folderStartRequest(t, app, "")
			if response.Code != http.StatusOK || readFolderResult(t, response).Path != root {
				t.Errorf("first open with %s: %d %s", name, response.Code, response.Body.String())
			}
		})
	}
}
