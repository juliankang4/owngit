package githttp

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A request with a declared length that passes the request limit stops Git
// at once. git-http-backend copies a declared length to Git and loops without
// end when its input closes early, so closing the input alone would keep it
// running, with the repository lock, until the operation limit.
func TestRequestLimitStopsGitAtOnce(t *testing.T) {
	handler, work, _ := idleFixture(t, 16, time.Minute)
	handler.MaximumRequest = 64 << 10
	logs := captureLog(t)
	returned := make(chan time.Duration, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		handler.ServeHTTP(writer, request)
		if request.Method == http.MethodPost {
			returned <- time.Since(started)
		}
	}))
	defer server.Close()

	// receive-pack reads the empty command list and exits while the rest of
	// the body is still arriving.
	body := append([]byte("0000"), bytes.Repeat([]byte("0"), 512<<10)...)
	for round := 0; round < 3; round++ {
		connection, err := net.Dial("tcp", server.Listener.Addr().String())
		noErr(t, err)
		_, err = fmt.Fprintf(connection, "POST /git/sample.git/git-receive-pack HTTP/1.1\r\nHost: example.test\r\n"+
			"Content-Type: application/x-git-receive-pack-request\r\nContent-Length: %d\r\n\r\n", len(body))
		noErr(t, err)
		go func() { _, _ = connection.Write(body) }()
		go func() { _, _ = io.Copy(io.Discard, connection) }()
		select {
		case elapsed := <-returned:
			if elapsed > 5*time.Second {
				t.Fatalf("round %d: the transfer ended after %s", round, elapsed)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: the transfer did not end; Git kept running after the request limit", round)
		}
		_ = connection.Close()
	}
	if got := strings.Count(logs.String(), `Git push request for repository "sample" failed: request body exceeded the size limit`); got != 3 {
		t.Fatalf("log names the request limit %d times, want 3: %s", got, logs.String())
	}

	// The repository is free: a push goes through at once.
	noErr(t, os.WriteFile(filepath.Join(work, "after.txt"), []byte("after\n"), 0o600))
	runHTTPGit(t, work, "add", ".")
	runHTTPGit(t, work, "commit", "-q", "-m", "after")
	started := time.Now()
	runHTTPGit(t, work, "push", "-q", server.URL+"/git/sample.git", "HEAD:refs/heads/main")
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("push after the refused requests took %s", elapsed)
	}
}
