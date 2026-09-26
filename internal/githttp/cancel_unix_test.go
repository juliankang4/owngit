//go:build !windows

package githttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestClientCancellationReapsBackendProcessTreeBeforeReleasingSlot(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	runner.TerminationGrace = 25 * time.Millisecond
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	backend := filepath.Join(root, "git-http-backend")
	script := "#!/bin/sh\nsleep 60 &\nchild=$!\nprintf '%s' \"$child\" > " + quoteShell(pidFile) + "\nwait \"$child\"\n"
	noErr(t, os.WriteFile(backend, []byte(script), 0o700))
	handler, err := New(runner, manager, backend, 1)
	noErr(t, err)
	handler.Authorize = func(*http.Request) bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "http://localhost/git/sample.git/info/refs?service=git-upload-pack", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()

	// The bounds below are hang guards; the child sleeps 60 seconds, so a
	// child left running still fails the last one.
	var childPID int
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(pidFile)
		if err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(content)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		cancel()
		<-done
		t.Fatal("backend child did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not return after cancellation")
	}
	if handler.Active() != 0 {
		t.Fatalf("handler released response but still reports %d active process(es)", handler.Active())
	}
	waitContext, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	noErr(t, handler.Wait(waitContext), "wait for owned Git operations")
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("backend child process %d remained after request cancellation", childPID)
}

func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// An abandoned operation whose client stalls its upload returns at once
// instead of at the operation deadline: when the response exceeds its limit,
// when the gzip body turns out corrupt mid-stream, and when the body passes
// the request size limit (QA-007).
func TestAbandonedOperationWithStalledUploadReturnsPromptly(t *testing.T) {
	root := t.TempDir()
	eager := filepath.Join(root, "git-http-backend")
	script := "#!/bin/sh\nprintf 'Content-Type: application/x-git-receive-pack-result\\r\\n\\r\\n'\nprintf '%064d' 0\nexec sleep 60\n"
	noErr(t, os.WriteFile(eager, []byte(script), 0o700))
	fake, err := os.Executable()
	noErr(t, err)
	gzipHeader := "\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff"
	for _, test := range []struct {
		name, backend, encoding, firstChunk string
		maximumRequest                      int64
	}{
		{"response over its limit", eager, "", "0000", 4 << 30},
		{"corrupt gzip body", fake, "Content-Encoding: gzip\r\n", gzipHeader + "\xff\xff\xff\xff", 4 << 30},
		{"request over its limit", fake, "", strings.Repeat("0", 64), 16},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, runner := newHTTPTestRepository(t)
			runner.TerminationGrace = 25 * time.Millisecond
			handler, err := New(runner, manager, test.backend, 1)
			noErr(t, err)
			handler.Authorize = func(*http.Request) bool { return true }
			handler.MaximumResponse = 16
			handler.MaximumRequest = test.maximumRequest
			// A handler that waited for the operation deadline would take 30
			// seconds, far beyond the 10-second bound below, however slowly a
			// busy machine starts the backend.
			handler.OperationTimeout = 30 * time.Second
			returned := make(chan time.Time, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				handler.ServeHTTP(writer, request)
				returned <- time.Now()
			}))
			defer server.Close()
			connection, err := net.Dial("tcp", server.Listener.Addr().String())
			noErr(t, err)
			defer connection.Close()
			started := time.Now()
			_, err = fmt.Fprintf(connection, "POST /git/sample.git/git-receive-pack HTTP/1.1\r\nHost: localhost\r\n"+
				"Content-Type: application/x-git-receive-pack-request\r\n%sTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n",
				test.encoding, len(test.firstChunk), test.firstChunk)
			noErr(t, err)
			select {
			case finished := <-returned:
				if elapsed := finished.Sub(started); elapsed > 10*time.Second {
					t.Fatalf("handler returned after %s, want well before the %s operation deadline", elapsed, handler.OperationTimeout)
				}
			case <-time.After(60 * time.Second):
				t.Fatal("handler did not return")
			}
			if handler.Active() != 0 {
				t.Fatalf("active=%d after the handler returned", handler.Active())
			}
		})
	}
}
