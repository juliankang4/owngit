package main

import (
	"context"
	"database/sql/driver"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
	"owngit/internal/gitexec"
	"owngit/internal/state"
)

// Every wait of a stop shares one deadline. Subsystems that never finish on
// their own must not add their allowances up: the stop ends at the deadline,
// says what was still running and gives up the process, instead of
// releasing storage under that work. This includes the running-network
// cleanup, which waits for the store's only SQLite connection.
func TestShutdownWaitsShareOneDeadline(t *testing.T) {
	if os.Getenv("OWNGIT_TEST_SHUTDOWN") != "1" {
		command := exec.Command(os.Args[0], "-test.run=^TestShutdownWaitsShareOneDeadline$", "-test.timeout=30s")
		command.Env = append(os.Environ(), "OWNGIT_TEST_SHUTDOWN=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("shutdown subprocess: %v\n%s", err, output)
		}
		return
	}
	var mu sync.Mutex
	var logged []string
	var exits []int
	logf := func(format string, arguments ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, format)
	}
	previous := exitProcess
	t.Cleanup(func() { exitProcess = previous })
	exitProcess = func(status int) {
		mu.Lock()
		defer mu.Unlock()
		exits = append(exits, status)
	}
	serving, endServing := context.WithCancel(context.Background())
	clock := newShutdownClock(serving, 200*time.Millisecond)
	blocked := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

	// A blocked state user holds the sole SQLite connection.
	entered := make(chan struct{})
	release := make(chan struct{})
	queryDone := make(chan error, 1)
	noErr(t, sqlite.RegisterScalarFunction("shutdown_test_hold_connection", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(entered)
		<-release
		return int64(1), nil
	}))
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer store.Close()
	_, clearNetwork, err := liveNetwork(store, true, serveNetwork{}, serveProxies{}, "127.0.0.1:32123", "http://127.0.0.1:32123", nil, nil, nil, t.Logf)
	noErr(t, err)
	go func() { queryDone <- store.Exec(context.Background(), "SELECT shutdown_test_hold_connection()") }()
	<-entered
	defer func() { close(release); <-queryDone }()

	// A process OwnGit started in its own process group must not outlive it.
	var childGone chan error
	if runtime.GOOS != "windows" {
		child := exec.Command("sleep", "60")
		gitexec.ConfigureOwnedProcess(child)
		noErr(t, child.Start())
		owner, err := gitexec.AttachOwnedProcess(child)
		noErr(t, err)
		defer gitexec.CloseOwnedProcess(owner)
		childGone = make(chan error, 1)
		go func() { childGone <- child.Wait() }()
	}

	endServing()
	started := time.Now()
	clock.stop(logf, "imports", blocked)
	clock.stop(logf, "clearing the running network", clearNetwork)
	clock.wait(logf, "activity", make(chan struct{}))
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("three blocked waits took %s, want about the one 200ms deadline", elapsed)
	}
	if childGone != nil {
		if err := <-childGone; err == nil {
			t.Fatal("the owned process ended normally, want it killed when OwnGit gave up")
		}
	}
	// After a serving error, giving up is a failure for the manager.
	failed := newShutdownClock(serving, 0)
	failed.status.Store(1)
	failed.stop(logf, "failing stop", blocked)
	mu.Lock()
	defer mu.Unlock()
	if len(exits) != 4 || exits[3] != 1 {
		t.Fatalf("exits %v, want status 1 after a serving error", exits)
	}
	exits = exits[:3]
	if len(exits) != 3 || exits[0] != 0 {
		t.Fatalf("gave up %v, want one exit with status 0 for each wait that did not finish", exits)
	}
	if !strings.Contains(logged[len(logged)-1], "still running at the shutdown deadline; exiting now") {
		t.Fatalf("log %q lacks what was still running", logged)
	}
}
