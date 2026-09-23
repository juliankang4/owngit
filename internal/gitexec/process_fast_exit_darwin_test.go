//go:build darwin

package gitexec

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestAttachOwnedProcessAcceptsExitedUnreapedChild(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
	ConfigureOwnedProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start synthetic child: %v", err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := syscall.Getpgid(cmd.Process.Pid)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if err != nil {
			t.Fatalf("observe synthetic child process group: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("synthetic child did not reach the exited, unreaped state")
		}
		time.Sleep(time.Millisecond)
	}
	if cmd.ProcessState != nil {
		t.Fatalf("synthetic child was already reaped: %v", cmd.ProcessState)
	}

	observerCalled := false
	owner, err := AttachOwnedProcessObserved(cmd, func() error {
		observerCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("attach exited, unreaped child: %v", err)
	}
	if !observerCalled {
		t.Fatal("attachment observer was not called")
	}
	if owner == nil || owner.pgid != cmd.Process.Pid {
		t.Fatalf("owner=%v, want process group %d", owner, cmd.Process.Pid)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for successfully launched child: %v", err)
	}
	waited = true
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatalf("synthetic child state=%v, want successful exit", cmd.ProcessState)
	}
	if err := CloseOwnedProcess(owner); err != nil {
		t.Fatalf("close owner: %v", err)
	}
}
