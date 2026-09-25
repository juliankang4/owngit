//go:build !windows

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/githttp"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// Stopping the server while a Git transfer is still streaming ends the
// transfer after the shutdown wait, logs it, and succeeds (QA-022).
func TestStopServingEndsARunningTransferAndSucceeds(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, "open", "", adminHash, true))
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	runner.TerminationGrace = 50 * time.Millisecond
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	_, err = manager.Create(ctx, "big", "")
	noErr(t, err)
	// A backend that streams without end stands in for a large clone.
	backend := filepath.Join(root, "git-http-backend")
	noErr(t, os.WriteFile(backend, []byte("#!/bin/sh\nprintf 'Content-Type: application/x-git-upload-pack-advertisement\\r\\n\\r\\n'\nexec yes\n"), 0o700))
	gitHandler, err := githttp.New(runner, manager, backend, 4)
	noErr(t, err)
	gitHandler.Authorize = func(*http.Request) bool { return true }
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	noErr(t, err)
	httpServer := &http.Server{Handler: gitHandler}
	go func() { _ = httpServer.Serve(listener) }()

	// The client asks for the transfer and then stops reading.
	connection, err := net.Dial("tcp", listener.Addr().String())
	noErr(t, err)
	defer connection.Close()
	_, err = fmt.Fprintf(connection, "GET /git/big.git/info/refs?service=git-upload-pack HTTP/1.1\r\nHost: localhost\r\n\r\n")
	noErr(t, err)
	deadline := time.Now().Add(5 * time.Second)
	for gitHandler.Active() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the transfer did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	var mu sync.Mutex
	var logged []string
	logf := func(format string, arguments ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, arguments...))
	}
	started := time.Now()
	if err := stopServing(httpServer, gitHandler, 300*time.Millisecond, logf); err != nil {
		t.Fatalf("stop with a running transfer failed: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("stop took %s", elapsed)
	}
	if gitHandler.Active() != 0 {
		t.Fatalf("active=%d after stop", gitHandler.Active())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 1 || !strings.Contains(logged[0], "ended 1 Git transfer(s)") {
		t.Fatalf("stop log %q, want one line about the ended transfer", logged)
	}
}
