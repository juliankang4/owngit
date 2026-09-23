package gitexec

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// The unattached-started-process cleanup must terminate only the started
// process, bound its wait by one effective grace and preserve every observed
// cause. A wait that is still pending at the bound is honest, not a failure
// that includes a future wait result.
func TestProcessCleanupUnattachedStartedProcess(t *testing.T) {
	attachErr := errors.New("injected attach failure")
	killErr := errors.New("injected kill failure")
	waitErr := errors.New("injected wait failure")

	t.Run("completed wait joins every observed cause", func(t *testing.T) {
		cmd := startStreamFixture(t, "hold-stdout")
		seam := &processCleanupSeam{
			killFunc: func(*exec.Cmd) error { return killErr },
			waitFunc: func(cmd *exec.Cmd) error {
				_ = cmd.Process.Kill()
				return errors.Join(waitErr, cmd.Wait())
			},
		}
		waited, err := cleanupUnattachedStartedProcess(cmd, 2*time.Second, attachErr, seam)
		if !waited {
			t.Fatalf("completed wait was reported pending: %v", err)
		}
		if errors.Is(err, errStartedProcessUnreaped) {
			t.Fatalf("completed wait reported unreaped: %v", err)
		}
		for name, want := range map[string]error{"attach": attachErr, "kill": killErr, "wait": waitErr} {
			if !errors.Is(err, want) {
				t.Fatalf("cleanup discarded the %s cause: %v", name, err)
			}
		}
	})

	t.Run("pending wait uses the default grace and stays releasable", func(t *testing.T) {
		cmd := startStreamFixture(t, "hold-stdout")
		entered := make(chan struct{})
		release := make(chan struct{})
		finished := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(unblock)
		seam := &processCleanupSeam{
			killFunc: func(*exec.Cmd) error { return killErr },
			waitFunc: func(cmd *exec.Cmd) error {
				defer close(finished)
				close(entered)
				<-release
				_ = cmd.Process.Kill()
				return errors.Join(waitErr, cmd.Wait())
			},
		}
		started := time.Now()
		waited, err := cleanupUnattachedStartedProcess(cmd, 0, attachErr, seam)
		elapsed := time.Since(started)
		if waited {
			t.Fatalf("pending wait was reported complete: %v", err)
		}
		select {
		case <-entered:
		default:
			t.Fatal("controlled wait was not entered")
		}
		if !errors.Is(err, errStartedProcessUnreaped) {
			t.Fatalf("pending wait timeout missing: %v", err)
		}
		if errors.Is(err, waitErr) {
			t.Fatalf("future wait failure was claimed: %v", err)
		}
		if !errors.Is(err, attachErr) || !errors.Is(err, killErr) {
			t.Fatalf("pending cleanup lost observed causes: %v", err)
		}
		if elapsed < 1500*time.Millisecond || elapsed > 5*time.Second {
			t.Fatalf("default grace was not applied: %s", elapsed)
		}
		unblock()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Fatal("delayed wait was not rejoined after release")
		}
	})
}

func startStreamFixture(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(streamTestExecutable(t))
	cmd.Env = append(os.Environ(), streamFixtureEnv+"="+mode)
	noErr(t, cmd.Start())
	return cmd
}
