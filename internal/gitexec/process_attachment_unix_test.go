//go:build !windows

package gitexec

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestAttachOwnedProcessRejectsUnsafeCommandState(t *testing.T) {
	configured := func(pid int) *exec.Cmd {
		return &exec.Cmd{
			Process:     &os.Process{Pid: pid},
			SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
		}
	}

	tests := []struct {
		name    string
		cmd     *exec.Cmd
		wantErr string
	}{
		{name: "nil command", wantErr: "has not started with a positive PID"},
		{name: "nil Process", cmd: &exec.Cmd{SysProcAttr: &syscall.SysProcAttr{Setpgid: true}}, wantErr: "has not started with a positive PID"},
		{name: "nonpositive PID", cmd: configured(0), wantErr: "has not started with a positive PID"},
		{name: "already reaped", cmd: func() *exec.Cmd {
			cmd := configured(42)
			cmd.ProcessState = &os.ProcessState{}
			return cmd
		}(), wantErr: "has already been reaped"},
		{name: "nil SysProcAttr", cmd: &exec.Cmd{Process: &os.Process{Pid: 42}}, wantErr: "not configured with a new process group"},
		{name: "Setpgid disabled", cmd: &exec.Cmd{
			Process:     &os.Process{Pid: 42},
			SysProcAttr: &syscall.SysProcAttr{},
		}, wantErr: "not configured with a new process group"},
		{name: "nonzero Pgid", cmd: &exec.Cmd{
			Process:     &os.Process{Pid: 42},
			SysProcAttr: &syscall.SysProcAttr{Setpgid: true, Pgid: 42},
		}, wantErr: "not configured with a new process group"},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			observerCalled := false
			owner, err := AttachOwnedProcessObserved(testCase.cmd, func() error {
				observerCalled = true
				return nil
			})
			if owner != nil || err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("owner=%v error=%v, want error containing %q", owner, err, testCase.wantErr)
			}
			if observerCalled {
				t.Fatal("observer ran for an unsafe command state")
			}
		})
	}
}
