package githttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// A clone or an archive whose repository storage cannot be read is answered
// 503, and the log names the cause once, quoted on the request's line.
func TestStorageFailureIsLoggedWithItsCause(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	path, err := manager.Path("sample")
	noErr(t, err)
	noErr(t, os.Rename(path, path+".moved"))
	logs := captureLog(t)
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Get(server.URL + "/git/sample.git/info/refs?service=git-upload-pack")
	noErr(t, err)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("clone status=%d, want 503", response.StatusCode)
	}
	archive := handler.ServeArchive(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/archive", nil),
		"sample", strings.Repeat("0", 40), ArchiveZip, "sample", "sample.zip")
	if failure, ok := archive.(*ArchiveError); !ok || failure.Status != http.StatusServiceUnavailable {
		t.Fatalf("archive error=%v, want 503", archive)
	}

	lines := strings.Split(strings.TrimSuffix(logs.String(), "\n"), "\n")
	for index, prefix := range []string{
		`Git fetch ref advertisement request for repository "sample" failed: repository storage is unavailable: "`,
		`Git archive request for repository "sample" failed: repository storage is unavailable: "`,
	} {
		if len(lines) != 2 || !strings.HasPrefix(lines[index], prefix) || !strings.HasSuffix(lines[index], `no such file or directory"`) {
			t.Fatalf("log=%q, want two lines, each with the quoted cause", logs.String())
		}
	}
}

// A cause is logged on its line, whatever its text holds, so Git's error
// output cannot start a line of its own.
func TestCauseIsLoggedOnOneLine(t *testing.T) {
	logs := captureLog(t)
	logCause(context.Background(), `Git push request for repository "sample": could not look for the objects of an unfinished push`,
		errors.New("open objects\nGit fetch request for repository \"other\" failed: \x1b[31mforged"))
	if got := logs.String(); strings.Count(got, "\n") != 1 || strings.Contains(got, "\x1b") ||
		!strings.HasSuffix(got, `: "open objects\nGit fetch request for repository \"other\" failed: \x1b[31mforged"`+"\n") {
		t.Fatalf("log=%q, want one line with the cause quoted", got)
	}
}

// A cause is left out of the log only when all of it is the request's own
// client going away. A cancellation joined with a real failure, or one from
// another context, is logged.
func TestOnlyTheClientLeavingIsNotLogged(t *testing.T) {
	logs := captureLog(t)
	left, cancel := context.WithCancel(context.Background())
	cancel()
	disk := errors.New("disk I/O error")
	logCause(left, "left", fmt.Errorf("read: %w", context.Canceled))
	if got := logs.String(); got != "" {
		t.Fatalf("a client that went away was logged: %q", got)
	}
	logCause(left, "joined", errors.Join(context.Canceled, disk))
	logCause(context.Background(), "other context", fmt.Errorf("read: %w", context.Canceled))
	if got := logs.String(); got != "joined: \"context canceled\\ndisk I/O error\"\nother context: \"read: context canceled\"\n" {
		t.Fatalf("log=%q, want the joined failure and the other context's cancellation", got)
	}
}
