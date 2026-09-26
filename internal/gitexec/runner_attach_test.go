package gitexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// errOutputNotReady means the test killed the child before the parent had
// copied the expected output. It keeps the empty-result check from passing
// only because the child had not run yet.
var errOutputNotReady = errors.New("child output was not copied before kill")

// exactChild reaps the specific process a test started, including when an
// assertion fails before a delayed Wait is released.
type exactChild struct {
	cmd      atomic.Pointer[exec.Cmd]
	owner    atomic.Pointer[ProcessOwner]
	waitOnce sync.Once
	waitErr  error
}

func (c *exactChild) remember(cmd *exec.Cmd) {
	if cmd != nil {
		c.cmd.Store(cmd)
	}
}

func (c *exactChild) rememberOwner(owner *ProcessOwner) {
	if owner != nil {
		c.owner.Store(owner)
	}
}

func (c *exactChild) reap(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("exact child process is unavailable")
	}
	c.waitOnce.Do(func() {
		c.waitErr = cmd.Wait()
	})
	return c.waitErr
}

func (c *exactChild) installCleanup(t *testing.T, unblock func()) {
	t.Helper()
	t.Cleanup(func() {
		if unblock != nil {
			unblock()
		}
		cmd := c.cmd.Load()
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = c.reap(cmd)
		}
		if owner := c.owner.Load(); owner != nil {
			_ = CloseOwnedProcess(owner)
		}
	})
}

// attachFailure resumes the child through the real attachment path, including
// a Windows CREATE_SUSPENDED process, then returns the injected failure. The
// owner stays open until cleanup so closing a Windows job does not kill the
// child before it can emit output.
func (c *exactChild) attachFailure(injected error) func(*exec.Cmd) (*ProcessOwner, error) {
	return func(cmd *exec.Cmd) (*ProcessOwner, error) {
		c.remember(cmd)
		owner, err := AttachOwnedProcess(cmd)
		c.rememberOwner(owner)
		return nil, errors.Join(injected, err)
	}
}

// killFailure calls the real process kill and returns the injected cause with
// the real result. before runs first so a caller can wait for output evidence.
func (c *exactChild) killFailure(injected error, before func(*exec.Cmd) error) func(*exec.Cmd) error {
	return func(cmd *exec.Cmd) error {
		c.remember(cmd)
		var beforeErr error
		if before != nil {
			beforeErr = before(cmd)
		}
		return errors.Join(injected, beforeErr, cmd.Process.Kill())
	}
}

type copyTap struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	needle []byte
	ready  chan struct{}
	once   sync.Once
}

func newCopyTap(needle string) (*copyTap, <-chan struct{}) {
	tap := &copyTap{needle: []byte(needle), ready: make(chan struct{})}
	return tap, tap.ready
}

func (t *copyTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.buf.Write(p)
	if bytes.Contains(t.buf.Bytes(), t.needle) {
		t.once.Do(func() { close(t.ready) })
	}
	return len(p), nil
}

func waitForCopiedOutput(ready <-chan struct{}) error {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ready:
		return nil
	case <-timer.C:
		return errOutputNotReady
	}
}

// A failed attachment inside Runner.run must preserve the observed causes. A
// completed wait leaves the copied output safe to return; a still-pending wait
// returns an honest empty result because the delayed Wait still writes the
// shared output buffers.
func TestRunAttachmentFailureKeepsOrDropsCopiedOutput(t *testing.T) {
	attachErr := errors.New("injected attach failure")
	killErr := errors.New("injected kill failure")
	waitErr := errors.New("injected wait failure")

	t.Run("completed wait returns copied output", func(t *testing.T) {
		runner := streamTestRunner(t, t.TempDir())
		runner.TerminationGrace = 2 * time.Second
		stdoutTap, stdoutReady := newCopyTap("stdout-payload")
		stderrTap, stderrReady := newCopyTap("stderr-payload")
		runner.stdoutCopyTap = stdoutTap
		runner.stderrCopyTap = stderrTap
		child := &exactChild{}
		child.installCleanup(t, nil)
		runner.processSeam = &processCleanupSeam{
			attachFunc: child.attachFailure(attachErr),
			killFunc: child.killFailure(killErr, func(*exec.Cmd) error {
				if err := waitForCopiedOutput(stdoutReady); err != nil {
					return err
				}
				return waitForCopiedOutput(stderrReady)
			}),
			waitFunc: func(cmd *exec.Cmd) error {
				return errors.Join(waitErr, child.reap(cmd))
			},
		}
		result, err := runner.RunWithLimits(context.Background(), t.TempDir(), nil, CommandLimits{
			Timeout: 2 * time.Second, Environment: []string{streamFixtureEnv + "=success"},
		})
		if errors.Is(err, errOutputNotReady) {
			t.Fatalf("output readiness was not established: %v", err)
		}
		if !errors.Is(err, attachErr) || !errors.Is(err, killErr) || !errors.Is(err, waitErr) {
			t.Fatalf("attachment cleanup lost observed causes: %v", err)
		}
		if errors.Is(err, errStartedProcessUnreaped) {
			t.Fatalf("completed wait reported unreaped: %v", err)
		}
		if string(result.Stdout) != "stdout-payload" || string(result.Stderr) != "stderr-payload" {
			t.Fatalf("completed wait lost copied output: stdout=%q stderr=%q", result.Stdout, result.Stderr)
		}
	})

	t.Run("pending wait returns the empty result", func(t *testing.T) {
		runner := streamTestRunner(t, t.TempDir())
		runner.TerminationGrace = 40 * time.Millisecond
		stdoutTap, stdoutReady := newCopyTap("ready\n")
		runner.stdoutCopyTap = stdoutTap
		entered := make(chan struct{})
		release := make(chan struct{})
		finished := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		child := &exactChild{}
		child.installCleanup(t, unblock)
		// The bound is measured from the start of cleanup, once the output
		// arrived, so a slow process start does not count against it.
		var cleanupStarted time.Time
		runner.processSeam = &processCleanupSeam{
			attachFunc: child.attachFailure(attachErr),
			killFunc: child.killFailure(killErr, func(*exec.Cmd) error {
				err := waitForCopiedOutput(stdoutReady)
				cleanupStarted = time.Now()
				return err
			}),
			waitFunc: func(cmd *exec.Cmd) error {
				defer close(finished)
				close(entered)
				<-release
				_ = cmd.Process.Kill()
				return errors.Join(waitErr, child.reap(cmd))
			},
		}
		result, err := runner.RunWithLimits(context.Background(), t.TempDir(), nil, CommandLimits{
			Timeout: 2 * time.Second, Environment: []string{streamFixtureEnv + "=hold-stdout"},
		})
		if elapsed := time.Since(cleanupStarted); cleanupStarted.IsZero() || elapsed > 3*time.Second {
			t.Fatalf("pending cleanup exceeded its bound: started=%v elapsed=%s", !cleanupStarted.IsZero(), elapsed)
		}
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("controlled wait was not entered")
		}
		if errors.Is(err, errOutputNotReady) {
			t.Fatalf("output readiness was not established: %v", err)
		}
		if !errors.Is(err, attachErr) || !errors.Is(err, killErr) {
			t.Fatalf("pending cleanup lost observed causes: %v", err)
		}
		if !errors.Is(err, errStartedProcessUnreaped) {
			t.Fatalf("pending wait timeout missing: %v", err)
		}
		if errors.Is(err, waitErr) {
			t.Fatalf("future wait failure was claimed: %v", err)
		}
		if len(result.Stdout) != 0 || len(result.Stderr) != 0 {
			t.Fatalf("pending wait returned unstable output: stdout=%q stderr=%q", result.Stdout, result.Stderr)
		}
		unblock()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Fatal("delayed wait was not rejoined after release")
		}
	})
}

// A failed attachment in Stream owns the pipes and must not admit the consumer.
func TestStreamAttachmentFailureSkipsConsumer(t *testing.T) {
	attachErr := errors.New("injected attach failure")
	killErr := errors.New("injected kill failure")
	waitErr := errors.New("injected wait failure")
	for _, stalled := range []bool{false, true} {
		name := "completed wait preserves causes"
		if stalled {
			name = "pending wait is bounded"
		}
		t.Run(name, func(t *testing.T) {
			runner := streamTestRunner(t, t.TempDir())
			runner.TerminationGrace = 40 * time.Millisecond
			entered := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			child := &exactChild{}
			child.installCleanup(t, unblock)
			runner.processSeam = &processCleanupSeam{
				attachFunc: child.attachFailure(attachErr),
				killFunc:   child.killFailure(killErr, nil),
				waitFunc: func(cmd *exec.Cmd) error {
					defer close(finished)
					if stalled {
						close(entered)
						<-release
					}
					_ = cmd.Process.Kill()
					return errors.Join(waitErr, child.reap(cmd))
				},
			}
			var consumed atomic.Int32
			stderr, err := runner.Stream(context.Background(), runner.GitPath, t.TempDir(), nil,
				[]string{streamFixtureEnv + "=hold-stdout"}, func(io.Reader) error {
					consumed.Add(1)
					return nil
				})
			if !errors.Is(err, attachErr) || !errors.Is(err, killErr) {
				t.Fatalf("attachment cleanup lost observed causes: %v", err)
			}
			if stderr != nil {
				t.Fatalf("attachment failure returned stderr bytes: %q", stderr)
			}
			if consumed.Load() != 0 {
				t.Fatalf("consumer was admitted after attachment failed: %d", consumed.Load())
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
				case <-finished:
				case <-time.After(10 * time.Second):
					t.Fatal("delayed wait was not rejoined after release")
				}
				return
			}
			if !errors.Is(err, waitErr) {
				t.Fatalf("completed wait failure was discarded: %v", err)
			}
		})
	}
}
