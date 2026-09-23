package gitexec

import (
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// errStartedProcessUnreaped reports that cleanup after a failed ownership
// attachment could not observe the started process exit inside one termination
// grace. The delayed reaper stays valid and may complete later, so output
// buffers shared with the command are still mutable.
var errStartedProcessUnreaped = errors.New("started process was not reaped within the termination grace")

// processCleanupSeam injects the attachment-failure cleanup operations for
// tests. A nil seam or a nil field uses the real operation.
type processCleanupSeam struct {
	attachFunc func(*exec.Cmd) (*ProcessOwner, error)
	killFunc   func(*exec.Cmd) error
	waitFunc   func(*exec.Cmd) error
}

func (s *processCleanupSeam) attach(cmd *exec.Cmd) (*ProcessOwner, error) {
	if s == nil || s.attachFunc == nil {
		return AttachOwnedProcess(cmd)
	}
	return s.attachFunc(cmd)
}

func (s *processCleanupSeam) kill(cmd *exec.Cmd) error {
	if s == nil || s.killFunc == nil {
		return cmd.Process.Kill()
	}
	return s.killFunc(cmd)
}

func (s *processCleanupSeam) wait(cmd *exec.Cmd) error {
	if s == nil || s.waitFunc == nil {
		return cmd.Wait()
	}
	return s.waitFunc(cmd)
}

// cleanupUnattachedStartedProcess handles a successful Start followed by an
// ownership attachment failure. It terminates only the process this invocation
// started, waits at most one effective termination grace, and joins the
// attachment cause, any kill failure and a completed wait failure. waited is
// false when the wait was still pending at the bound; errStartedProcessUnreaped
// is then joined and the delayed reaper stays valid. Grace defaults like the
// other owned-process cleanup paths.
func cleanupUnattachedStartedProcess(cmd *exec.Cmd, grace time.Duration, attachErr error, seam *processCleanupSeam) (waited bool, err error) {
	if grace <= 0 {
		grace = 2 * time.Second
	}
	if cmd == nil || cmd.Process == nil {
		return false, errors.Join(attachErr, errors.New("started process identity is unavailable"))
	}
	var cleanupErr error
	if killErr := seam.kill(cmd); killErr != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("kill unattached started process: %w", killErr))
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- seam.wait(cmd) }()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case waitErr := <-waitCh:
		waited = true
		if waitErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("reap unattached started process: %w", waitErr))
		}
	case <-timer.C:
		cleanupErr = errors.Join(cleanupErr, errStartedProcessUnreaped)
	}
	return waited, errors.Join(attachErr, cleanupErr)
}
