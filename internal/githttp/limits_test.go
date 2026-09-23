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
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	pathInfo := os.Getenv("PATH_INFO")
	if os.Getenv("GIT_HTTP_EXPORT_ALL") == "1" && os.Getenv("SCRIPT_NAME") == "/git" && strings.HasPrefix(pathInfo, "/sample.git/") {
		runPortableFakeBackend()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runPortableFakeBackend() {
	if os.Getenv("REQUEST_METHOD") == http.MethodPost {
		_, _ = io.Copy(io.Discard, os.Stdin)
		_, _ = io.WriteString(os.Stdout, "Content-Type: application/x-git-receive-pack-result\r\n\r\n0000")
		return
	}
	_, _ = io.WriteString(os.Stdout, "Content-Type: application/x-git-upload-pack-advertisement\r\n\r\n")
	block := []byte("0123456789abcdef0123456789abcdef")
	for {
		if _, err := os.Stdout.Write(block); err != nil {
			return
		}
	}
}

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
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	backend, err := os.Executable()
	noErr(t, err)
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
	noErr(t, handler.Wait(waitContext), "Wait after timed-out request")
}

func TestStalledNetworkResponseHitsWriteDeadlineAndReapsOperation(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	backend, err := os.Executable()
	noErr(t, err)
	handler.BackendPath = backend
	handler.Authorize = func(*http.Request) bool { return true }
	handler.OperationTimeout = 75 * time.Millisecond
	runner.TerminationGrace = 25 * time.Millisecond
	server := httptest.NewServer(handler)
	defer server.Close()
	connection, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	noErr(t, err)
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
	noErr(t, handler.Wait(waitContext), "Wait after stalled response")
}

func TestUploadLimitRejectsPushWithoutChangingRef(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	server := httptest.NewServer(handler)
	defer server.Close()
	work := filepath.Join(t.TempDir(), "work")
	runHTTPGit(t, "", "init", "--initial-branch=main", work)
	runHTTPGit(t, work, "config", "user.name", "Limit Test")
	runHTTPGit(t, work, "config", "user.email", "limit@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "small"), []byte("small"), 0o600))
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
	noErr(t, os.WriteFile(filepath.Join(work, "large.bin"), payload, 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-m", "too large")
	if output, err := httpGitCombined(work, "-c", "http.postBuffer=1", "push", "origin", "HEAD:refs/heads/main"); err == nil {
		t.Fatalf("oversized push succeeded: %s", output)
	}
	remotePath, err := manager.Path("sample")
	noErr(t, err)
	got := httpGitOutput(t, "", "--git-dir", remotePath, "rev-parse", "refs/heads/main")
	if got != old {
		t.Fatalf("oversized push changed public ref to %s, want %s", got, old)
	}
}
