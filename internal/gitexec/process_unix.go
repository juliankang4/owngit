//go:build !windows

package gitexec

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

type ProcessOwner struct {
	pgid int
}

func ConfigureOwnedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func AttachOwnedProcess(cmd *exec.Cmd) (*ProcessOwner, error) {
	return AttachOwnedProcessObserved(cmd, nil)
}

func AttachOwnedProcessObserved(cmd *exec.Cmd, observeStarted func() error) (*ProcessOwner, error) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil, errors.New("attach owned process: command has not started with a positive PID")
	}
	if cmd.ProcessState != nil {
		return nil, errors.New("attach owned process: command has already been reaped")
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pgid != 0 {
		return nil, errors.New("attach owned process: command is not configured with a new process group")
	}

	// Setpgid with Pgid zero creates a group whose ID is the child PID before
	// exec. A successful Start therefore establishes ownership without a
	// post-start Getpgid call, which races a fast child exit on macOS.
	owner := &ProcessOwner{pgid: cmd.Process.Pid}
	if observeStarted != nil {
		if err := observeStarted(); err != nil {
			cleanupErr := errors.Join(err, TerminateOwnedProcess(owner, 0), CloseOwnedProcess(owner))
			return nil, fmt.Errorf("observe started process: %w", cleanupErr)
		}
	}
	return owner, nil
}

// TerminateOwnedProcess signals the owned process group and reports whether the
// group could be confirmed gone. A group that already exited is not a failure,
// so ESRCH is ignored. macOS reports EPERM for a group that only contains
// zombies, which is also gone for our purposes.
func TerminateOwnedProcess(owner *ProcessOwner, grace time.Duration) error {
	if owner == nil || owner.pgid <= 0 {
		return nil
	}
	var cleanupErr error
	if err := syscall.Kill(-owner.pgid, syscall.SIGTERM); err != nil {
		if groupGone(err) {
			return nil
		}
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("terminate owned process group: %w", err))
	}
	deadline := time.Now().Add(grace)
	for grace > 0 && time.Now().Before(deadline) {
		if groupGone(syscall.Kill(-owner.pgid, 0)) {
			return cleanupErr
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(-owner.pgid, syscall.SIGKILL); err != nil {
		if groupGone(err) {
			return cleanupErr
		}
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("kill owned process group: %w", err))
	}
	killWait := grace
	if killWait <= 0 {
		killWait = 100 * time.Millisecond
	}
	deadline = time.Now().Add(killWait)
	for time.Now().Before(deadline) {
		if groupGone(syscall.Kill(-owner.pgid, 0)) {
			return cleanupErr
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !groupGone(syscall.Kill(-owner.pgid, 0)) {
		cleanupErr = errors.Join(cleanupErr, errors.New("the owned process group survived SIGKILL"))
	}
	return cleanupErr
}

// groupGone reports whether a signal error means no live process is left.
func groupGone(err error) bool {
	return err == syscall.ESRCH || err == syscall.EPERM
}

// CloseOwnedProcess releases the owner. The POSIX owner holds no handle.
func CloseOwnedProcess(_ *ProcessOwner) error { return nil }
