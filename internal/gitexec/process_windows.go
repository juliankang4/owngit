//go:build windows

package gitexec

import (
	"fmt"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processOwner struct {
	job windows.Handle
}

func configureOwnedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func attachOwnedProcess(cmd *exec.Cmd) (*processOwner, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("assign process to job object: %w", err)
	}
	return &processOwner{job: job}, nil
}

func terminateOwnedProcess(owner *processOwner, _ time.Duration) {
	if owner != nil && owner.job != 0 {
		_ = windows.TerminateJobObject(owner.job, 1)
	}
}

func closeOwnedProcess(owner *processOwner) {
	if owner != nil && owner.job != 0 {
		_ = windows.CloseHandle(owner.job)
		owner.job = 0
	}
}
