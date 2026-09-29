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

// Chooser fixtures are moved to Trash where available, otherwise retained.
func folderFixture(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "owngit-folder-test-")
	noErr(t, err)
	t.Cleanup(func() {
		home, err := os.UserHomeDir()
		trash := filepath.Join(home, ".Trash")
		if info, statErr := os.Stat(trash); err != nil || statErr != nil || !info.IsDir() {
			t.Logf("retained folder fixture: %s", root)
			return
		}
		if err := os.Rename(root, filepath.Join(trash, filepath.Base(root))); err != nil {
			t.Logf("retained folder fixture: %s (%v)", root, err)
		}
	})
	return root
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
	result, err := listFolders(context.Background(), root, true)
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
	empty, err := listFolders(context.Background(), root, false)
	noErr(t, err)
	if len(empty.Folders) != 1 {
		t.Fatalf("fixture list=%+v", empty)
	}
	large := filepath.Join(root, "large")
	noErr(t, os.Mkdir(large, 0o700))
	for i := 0; i < folderEntryLimit+1; i++ {
		noErr(t, os.Mkdir(filepath.Join(large, fmt.Sprintf("folder-%04d", i)), 0o700))
	}
	result, err := listFolders(context.Background(), large, false)
	noErr(t, err)
	if !result.Truncated || len(result.Folders) != folderEntryLimit {
		t.Fatalf("bounded listing=%+v", result)
	}
	for i := 1; i < len(result.Folders); i++ {
		if result.Folders[i-1].Name >= result.Folders[i].Name {
			t.Fatal("listing not sorted")
		}
	}
	result, err = listFolders(context.Background(), filepath.Join(large, "folder-0000"), false)
	noErr(t, err)
	if result.Truncated || result.Folders == nil || len(result.Folders) != 0 {
		t.Fatalf("empty listing=%+v", result)
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
		status, code := folderProblem(context.DeadlineExceeded, create)
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
