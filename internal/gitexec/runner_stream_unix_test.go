//go:build !windows

package gitexec

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStreamEarlyConsumerErrorReapsBeforeTermination reproduces the Linux
// ordering defect: terminating an unreaped group leader keeps the group alive
// through both grace periods. Wait must start before termination so the leader
// is reaped first. The defect waits out both grace intervals; the correct
// order returns at once, so a long grace keeps the two far apart on a busy
// machine.
func TestStreamEarlyConsumerErrorReapsBeforeTermination(t *testing.T) {
	root := t.TempDir()
	runner := streamTestRunner(t, root)
	runner.TerminationGrace = 5 * time.Second
	pidFile := filepath.Join(root, "backend.pid")
	script := filepath.Join(root, "backend")
	content := "#!/bin/sh\nprintf '%s' \"$$\" > " + shellEscape(pidFile) + "\nprintf 'ready\\n'\nexec sleep 60\n"
	noErr(t, os.WriteFile(script, []byte(content), 0o700))
	consumerErr := errors.New("consumer stopped early")
	consumerReturned := make(chan time.Time, 1)
	_, err := runner.Stream(context.Background(), script, root, nil, nil, func(reader io.Reader) error {
		buffer := make([]byte, len("ready\n"))
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return err
		}
		consumerReturned <- time.Now()
		return consumerErr
	})
	abortElapsed := time.Since(<-consumerReturned)
	if !errors.Is(err, consumerErr) {
		t.Fatalf("Stream error=%v, want consumer sentinel", err)
	}
	t.Logf("abort path returned after %v", abortElapsed)
	if abortElapsed > runner.TerminationGrace {
		t.Fatalf("abort path took %v, a grace interval of %v or more; Wait must reap the leader before termination", abortElapsed, runner.TerminationGrace)
	}
	waitForStreamGroupGone(t, readStreamPID(t, pidFile))
}

// TestStreamCancellationWithPipeHoldingDescendantTerminatesOnce covers the
// cancellation path where a descendant keeps stdout open. The context error
// must survive, the group must be removed, and termination must run once even
// when the canceled context also wins the later select.
func TestStreamCancellationWithPipeHoldingDescendantTerminatesOnce(t *testing.T) {
	root := t.TempDir()
	runner := streamTestRunner(t, root)
	runner.TerminationGrace = 50 * time.Millisecond
	pidFile := filepath.Join(root, "backend.pid")
	childPIDFile := filepath.Join(root, "child.pid")
	script := filepath.Join(root, "backend")
	content := "#!/bin/sh\nprintf '%s' \"$$\" > " + shellEscape(pidFile) +
		"\nsleep 60 &\nchild=$!\nprintf '%s' \"$child\" > " + shellEscape(childPIDFile) +
		"\nprintf 'ready\\n'\nwait \"$child\"\n"
	noErr(t, os.WriteFile(script, []byte(content), 0o700))
	terminations := 0
	originalTerminate := streamTerminateOwnedProcess
	streamTerminateOwnedProcess = func(owner *ProcessOwner, grace time.Duration) error {
		terminations++
		return TerminateOwnedProcess(owner, grace)
	}
	t.Cleanup(func() { streamTerminateOwnedProcess = originalTerminate })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumerStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := runner.Stream(ctx, script, root, nil, nil, func(reader io.Reader) error {
			buffer := make([]byte, len("ready\n"))
			if _, err := io.ReadFull(reader, buffer); err != nil {
				return err
			}
			close(consumerStarted)
			_, err := io.Copy(io.Discard, reader)
			return err
		})
		done <- err
	}()
	awaitStreamConsumerStart(t, consumerStarted, done, cancel)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream error=%v, want context cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stream did not return after cancellation")
	}
	if terminations != 1 {
		t.Fatalf("terminations=%d, want exactly 1", terminations)
	}
	waitForStreamProcessGone(t, readStreamPID(t, pidFile))
	waitForStreamProcessGone(t, readStreamPID(t, childPIDFile))
}

// readStreamPID waits for a fixture's PID file; the bound is a hang guard.
func readStreamPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("PID file %s was not written", path)
	return 0
}

// waitForStreamProcessGone and waitForStreamGroupGone allow a busy machine 10
// seconds; the fixtures they watch would otherwise sleep for 60.
func waitForStreamProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d remained after Stream returned", pid)
}

func waitForStreamGroupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("owned process group %d remained after Stream returned", pgid)
}
