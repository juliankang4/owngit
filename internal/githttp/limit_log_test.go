package githttp

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// A transfer cut by the response size limit or by the operation time limit
// logs why, so the documented limits can be recognised in the server log
// (QA-020).
func TestTransferLimitsAreLogged(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	backend, err := os.Executable()
	noErr(t, err)
	handler.BackendPath = backend
	handler.Authorize = func(*http.Request) bool { return true }
	runner.TerminationGrace = 25 * time.Millisecond
	server := httptest.NewServer(handler)
	defer server.Close()
	logs := captureLog(t)
	waitForLine := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(logs.String(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("log %q lacks %q", logs.String(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	handler.MaximumResponse = 64 << 10
	response, err := http.Get(server.URL + "/git/sample.git/info/refs?service=git-upload-pack")
	noErr(t, err)
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	waitForLine(`Git fetch ref advertisement request for repository "sample" failed: response exceeded the size limit`)

	handler.MaximumResponse = 4 << 30
	handler.OperationTimeout = 100 * time.Millisecond
	connection, err := net.Dial("tcp", server.Listener.Addr().String())
	noErr(t, err)
	defer connection.Close()
	_, err = io.WriteString(connection, "GET /git/sample.git/info/refs?service=git-upload-pack HTTP/1.1\r\nHost: example.test\r\n\r\n")
	noErr(t, err)
	waitForLine(`Git fetch ref advertisement request for repository "sample" failed: operation timed out`)
}
