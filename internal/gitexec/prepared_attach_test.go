package gitexec

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A prepared update whose ownership attachment fails before the protocol
// goroutine starts must clean the started Git process within one grace,
// preserve the attachment, kill and any completed wait causes, and admit no
// callback. A still-pending wait is reported honestly and released for fixture
// cleanup.
func TestPreparedAttachmentFailureCleanupIsBoundedAndPreservesCauses(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		name := "completed wait preserves causes"
		if stalled {
			name = "pending wait is bounded and honest"
		}
		t.Run(name, func(t *testing.T) {
			runner := newPreparedHelperRunner(t)
			runner.TerminationGrace = 40 * time.Millisecond
			attachErr := errors.New("injected attach failure")
			killErr := errors.New("injected kill failure")
			waitErr := errors.New("injected wait failure")
			entered := make(chan struct{})
			release := make(chan struct{})
			waitFinished := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			// With a completed wait, the helper is killed and reaped before the
			// cleanup starts, so the cleanup grace covers only the seam calls
			// and not how long a loaded machine takes to end a process.
			var reaped error
			runner.processSeam = &processCleanupSeam{
				attachFunc: func(cmd *exec.Cmd) (*ProcessOwner, error) {
					if !stalled {
						_ = cmd.Process.Kill()
						reaped = cmd.Wait()
					}
					return nil, attachErr
				},
				killFunc: func(*exec.Cmd) error { return killErr },
				waitFunc: func(cmd *exec.Cmd) error {
					defer close(waitFinished)
					if !stalled {
						return errors.Join(waitErr, reaped)
					}
					close(entered)
					<-release
					_ = cmd.Process.Kill()
					return errors.Join(waitErr, cmd.Wait())
				},
			}
			var callbacks atomic.Int32
			result := make(chan error, 1)
			go func() {
				_, err := runner.RunPreparedUpdateContext(context.Background(), t.TempDir(),
					[]string{"verify refs/heads/main 0000000000000000000000000000000000000000"},
					CommandLimits{Timeout: 120 * time.Millisecond, Environment: []string{preparedHelperModeEnvironment + "=normal"}},
					func(context.Context) error { callbacks.Add(1); return nil })
				result <- err
			}()
			if stalled {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("controlled attachment cleanup was not reached")
				}
			}
			var err error
			select {
			case err = <-result:
			case <-time.After(3 * time.Second):
				t.Fatal("attachment cleanup did not return within its bound")
			}
			if callbacks.Load() != 0 {
				t.Fatalf("callback was admitted after attachment failed: %d", callbacks.Load())
			}
			if !errors.Is(err, attachErr) || !errors.Is(err, killErr) {
				t.Fatalf("attachment cleanup lost observed causes: %v", err)
			}
			if stalled {
				if !errors.Is(err, errStartedProcessUnreaped) {
					t.Fatalf("pending wait timeout missing: %v", err)
				}
				if errors.Is(err, waitErr) {
					t.Fatalf("future wait failure was claimed: %v", err)
				}
				unblock()
				select {
				case <-waitFinished:
				case <-time.After(3 * time.Second):
					t.Fatal("delayed wait was not rejoined after release")
				}
				return
			}
			if !errors.Is(err, waitErr) {
				t.Fatalf("completed wait failure was discarded: %v", err)
			}
			select {
			case <-waitFinished:
			default:
				t.Fatal("completed wait was not observed before the return")
			}
		})
	}
}
