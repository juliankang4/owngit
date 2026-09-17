//go:build !windows

package gitexec

import (
	"os/exec"
	"syscall"
	"time"
)

type processOwner struct {
	pgid int
}

func configureOwnedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attachOwnedProcess(cmd *exec.Cmd) (*processOwner, error) {
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return nil, err
	}
	return &processOwner{pgid: pgid}, nil
}

func terminateOwnedProcess(owner *processOwner, grace time.Duration) {
	if owner == nil || owner.pgid <= 0 {
		return
	}
	_ = syscall.Kill(-owner.pgid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for grace > 0 && time.Now().Before(deadline) {
		if err := syscall.Kill(-owner.pgid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(-owner.pgid, syscall.SIGKILL)
}

func closeOwnedProcess(_ *processOwner) {}
