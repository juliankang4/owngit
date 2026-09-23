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
	noErr(t, cmd.Start(), "start synthetic child")
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
		noErr(t, err, "observe synthetic child process group")
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
	noErr(t, err, "attach exited, unreaped child")
	if !observerCalled {
		t.Fatal("attachment observer was not called")
	}
	if owner == nil || owner.pgid != cmd.Process.Pid {
		t.Fatalf("owner=%v, want process group %d", owner, cmd.Process.Pid)
	}
	noErr(t, cmd.Wait(), "wait for successfully launched child")
	waited = true
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatalf("synthetic child state=%v, want successful exit", cmd.ProcessState)
	}
	noErr(t, CloseOwnedProcess(owner), "close owner")
}
