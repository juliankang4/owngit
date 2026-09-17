package githttp

import (
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type stalledRequestBody struct {
	once   sync.Once
	closed chan struct{}
}

func (body *stalledRequestBody) Read(buffer []byte) (int, error) {
	wrote := 0
	body.once.Do(func() { wrote = copy(buffer, "0000") })
	if wrote != 0 {
		return wrote, nil
	}
	<-body.closed
	return 0, io.ErrClosedPipe
}

func (body *stalledRequestBody) Close() error {
	select {
	case <-body.closed:
	default:
		close(body.closed)
	}
	return nil
}

func TestStalledChunkedBodyTimesOutAndReapsOperation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic shell backend is Unix-only")
	}
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(t.TempDir(), "stalled-backend")
	if err := os.WriteFile(backend, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'Content-Type: application/x-git-receive-pack-result\\r\\n\\r\\n0000'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	handler.BackendPath = backend
	handler.Authorize = func(*http.Request) bool { return true }
	handler.OperationTimeout = 75 * time.Millisecond
	runner.TerminationGrace = 25 * time.Millisecond
	body := &stalledRequestBody{closed: make(chan struct{})}
	request := httptest.NewRequest(http.MethodPost, "http://example.test/git/sample.git/git-receive-pack", body)
	request.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	request.TransferEncoding = []string{"chunked"}
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled chunked request did not time out")
	}
	select {
	case <-body.closed:
	default:
		t.Fatal("timed-out request body was not closed")
	}
	if active := handler.Active(); active != 0 {
		t.Fatalf("active Git processes = %d, want 0", active)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := handler.Wait(waitContext); err != nil {
		t.Fatalf("Wait after timed-out request: %v", err)
	}
}

func TestStalledNetworkResponseHitsWriteDeadlineAndReapsOperation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic shell backend is Unix-only")
	}
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(t.TempDir(), "endless-backend")
	script := "#!/bin/sh\nprintf 'Content-Type: application/x-git-upload-pack-advertisement\\r\\n\\r\\n'\nwhile :; do printf '0123456789abcdef0123456789abcdef'; done\n"
	if err := os.WriteFile(backend, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	handler.BackendPath = backend
	handler.Authorize = func(*http.Request) bool { return true }
	handler.OperationTimeout = 75 * time.Millisecond
	runner.TerminationGrace = 25 * time.Millisecond
	server := httptest.NewServer(handler)
	defer server.Close()
	connection, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := io.WriteString(connection, "GET /git/sample.git/info/refs?service=git-upload-pack HTTP/1.1\r\nHost: example.test\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for handler.Active() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if handler.Active() == 0 {
		t.Fatal("endless response operation did not start")
	}
	deadline = time.Now().Add(2 * time.Second)
	for handler.Active() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if active := handler.Active(); active != 0 {
		t.Fatalf("write deadline left %d active Git operation(s)", active)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := handler.Wait(waitContext); err != nil {
		t.Fatalf("Wait after stalled response: %v", err)
	}
}

func TestUploadLimitRejectsPushWithoutChangingRef(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	handler.Authorize = func(*http.Request) bool { return true }
	server := httptest.NewServer(handler)
	defer server.Close()
	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "Limit Test")
	runHTTPGit(t, work, "config", "user.email", "limit@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "small"), []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-m", "small")
	remoteURL := server.URL + "/git/sample.git"
	runHTTPGit(t, work, "remote", "add", "origin", remoteURL)
	runHTTPGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	old := httpGitOutput(t, work, "rev-parse", "HEAD")

	handler.MaximumRequest = 64 << 10
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "large.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-m", "too large")
	if output, err := httpGitCombined(work, "-c", "http.postBuffer=1", "push", "origin", "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("oversized push succeeded: %s", output)
	}
	remotePath, err := manager.Path("sample")
	if err != nil {
		t.Fatal(err)
	}
	got := httpGitOutput(t, "", "--git-dir", remotePath, "rev-parse", "refs/heads/main")
	if got != old {
		t.Fatalf("oversized push changed public ref to %s, want %s", got, old)
	}
}
