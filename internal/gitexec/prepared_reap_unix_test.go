//go:build !windows

package gitexec

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

// Linux reports a group whose leader exited but was not reaped as live:
// kill(-pgid, 0) succeeds for a zombie. macOS returns EPERM, which
// TerminateOwnedProcess accepts as gone, so this seam applies the Linux rule
// on every Unix host: the group counts as gone only after ESRCH, which needs
// the leader to be reaped by Wait.
func linuxZombieTerminate(owner *ProcessOwner, grace time.Duration) error {
	if err := syscall.Kill(-owner.pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-owner.pgid, 0) == syscall.ESRCH {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return errors.New("the owned process group survived SIGKILL")
}

// A prepared update that hangs after its final command is terminated while
// Wait already runs, so the killed leader is reaped during termination and the
// timeout reports only the deadline, promptly.
func TestPreparedUpdateTerminationReapsLeaderWhileSignaling(t *testing.T) {
	previous := preparedTerminateOwnedProcess
	preparedTerminateOwnedProcess = linuxZombieTerminate
	defer func() { preparedTerminateOwnedProcess = previous }()

	runner := newPreparedHelperRunner(t)
	runner.TerminationGrace = 500 * time.Millisecond
	started := time.Now()
	_, err := runner.RunPreparedUpdateContext(context.Background(), t.TempDir(), []string{"verify refs/heads/main 0000000000000000000000000000000000000000"}, CommandLimits{
		Timeout: 50 * time.Millisecond, Environment: []string{preparedHelperModeEnvironment + "=hang-final"},
	}, func(context.Context) error { return nil })
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung prepared update err=%v", err)
	}
	if errors.Is(err, ErrPreparedProcessNotReaped) {
		t.Fatalf("killed leader was not reaped: %v", err)
	}
	for unwrapped := []error{err}; len(unwrapped) > 0; {
		current := unwrapped[0]
		unwrapped = unwrapped[1:]
		if current.Error() == "the owned process group survived SIGKILL" {
			t.Fatalf("termination reported a false SIGKILL survival: %v", err)
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			unwrapped = append(unwrapped, joined.Unwrap()...)
		}
	}
	// One grace interval for the protocol, then prompt termination.
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("termination took %s", elapsed)
	}
}
