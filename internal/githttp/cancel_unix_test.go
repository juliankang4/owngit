//go:build !windows

package githttp

import (
	"context"
	"errors"
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
	if err := os.WriteFile(backend, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	handler, err := New(runner, manager, backend, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler.Authorize = func(*http.Request) bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "http://localhost/git/sample.git/info/refs?service=git-upload-pack", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()

	var childPID int
	deadline := time.Now().Add(3 * time.Second)
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
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return after cancellation")
	}
	if handler.Active() != 0 {
		t.Fatalf("handler released response but still reports %d active process(es)", handler.Active())
	}
	waitContext, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := handler.Wait(waitContext); err != nil {
		t.Fatalf("wait for owned Git operations: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
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
