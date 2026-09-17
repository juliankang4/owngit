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

func TestStreamCancellationAfterStdoutEOFStillReapsProcess(t *testing.T) {
	root := t.TempDir()
	runner, err := New("", filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	runner.TerminationGrace = 25 * time.Millisecond
	script := filepath.Join(root, "backend")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec 1>&-\nsleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = runner.Stream(ctx, script, root, nil, nil, func(reader io.Reader) error {
		_, err := io.Copy(io.Discard, reader)
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stream error=%v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Stream took %v to reap a process after stdout EOF", elapsed)
	}
}

func TestStreamCancellationTerminatesOwnedProcessGroup(t *testing.T) {
	root := t.TempDir()
	runner, err := New("", filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	runner.TerminationGrace = 25 * time.Millisecond
	pidFile := filepath.Join(root, "child.pid")
	script := filepath.Join(root, "backend")
	content := "#!/bin/sh\nsleep 60 &\nchild=$!\nprintf '%s' \"$child\" > " + shellEscape(pidFile) + "\nwait \"$child\"\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runner.Stream(ctx, script, root, nil, nil, func(reader io.Reader) error {
			_, err := io.Copy(io.Discard, reader)
			return err
		})
		done <- err
	}()

	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		cancel()
		<-done
		t.Fatal("child process did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream error=%v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stream did not return after cancellation")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child process %d remained after cancellation", childPID)
}

func shellEscape(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
