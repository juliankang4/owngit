//go:build windows

package gitexec

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsOwnerFailureFixture = "OWNGIT_GITEXEC_OWNER_FAILURE_FIXTURE"
	windowsOwnerFailureReady   = "OWNGIT_GITEXEC_OWNER_FAILURE_READY"
)

func TestWindowsOwnedProcessFailureFixture(t *testing.T) {
	if os.Getenv(windowsOwnerFailureFixture) == "" {
		return
	}
	if err := os.WriteFile(os.Getenv(windowsOwnerFailureReady), []byte("ready\n"), 0o600); err != nil {
		os.Exit(91)
	}
	time.Sleep(30 * time.Second)
	os.Exit(92)
}

func TestAttachOwnedProcessObserverFailureTerminatesStartedProcess(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsOwnedProcessFailureFixture$")
	cmd.Env = append(os.Environ(),
		windowsOwnerFailureFixture+"=1",
		windowsOwnerFailureReady+"="+ready,
	)
	wait := make(chan error, 1)
	waitHandled := false
	var owner *ProcessOwner
	ConfigureOwnedProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var cleanupErr error
		if owner != nil {
			cleanupErr = errors.Join(cleanupErr, TerminateOwnedProcess(owner, 0), CloseOwnedProcess(owner))
		}
		if waitHandled {
			if cleanupErr != nil {
				t.Errorf("fixture owner cleanup: %v", cleanupErr)
			}
			return
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("kill exact fixture process: %w", err))
		}
		_, _, waitCleanupErr := awaitWindowsTestProcess(cmd.Process, wait, 5*time.Second)
		cleanupErr = errors.Join(cleanupErr, waitCleanupErr)
		if cleanupErr != nil {
			t.Errorf("fixture process cleanup: %v", cleanupErr)
		}
	})
	go func() { wait <- cmd.Wait() }()

	observerErr := errors.New("observer rejected startup")
	observerCalled := false
	owner, err := AttachOwnedProcessObserved(cmd, func() error {
		observerCalled = true
		if !waitForWindowsTestFile(ready, 5*time.Second) {
			return errors.New("long-lived fixture did not report ready")
		}
		running, err := windowsTestProcessRunning(cmd.Process)
		if err != nil {
			return err
		}
		if !running {
			return errors.New("long-lived fixture exited before observer failure")
		}
		return observerErr
	})

	waitErr, exited, cleanupErr := awaitWindowsTestProcess(cmd.Process, wait, 5*time.Second)
	waitHandled = true
	if cleanupErr != nil {
		t.Fatalf("observer failure process cleanup: %v", cleanupErr)
	}
	if !exited {
		t.Fatal("long-lived fixture remained alive after observer failure cleanup")
	}
	if waitErr == nil {
		t.Fatal("long-lived fixture reported a successful natural exit after observer failure")
	}
	if owner != nil {
		t.Fatalf("owner=%+v, want nil after attachment failure", owner)
	}
	if !observerCalled {
		t.Fatal("startup observer was not called")
	}
	if !errors.Is(err, observerErr) || !strings.Contains(err.Error(), "observe started process") {
		t.Fatalf("attachment error=%v, want observer failure with context", err)
	}
}

func TestTerminateOwnedProcessStillTerminatesAfterCaptureDeadline(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsOwnedProcessFailureFixture$")
	cmd.Env = append(os.Environ(),
		windowsOwnerFailureFixture+"=1",
		windowsOwnerFailureReady+"="+ready,
	)
	wait := make(chan error, 1)
	waitHandled := false
	var owner *ProcessOwner
	ConfigureOwnedProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var cleanupErr error
		if owner != nil {
			cleanupErr = errors.Join(cleanupErr, TerminateOwnedProcess(owner, 0), CloseOwnedProcess(owner))
		}
		if waitHandled {
			if cleanupErr != nil {
				t.Errorf("fixture owner cleanup: %v", cleanupErr)
			}
			return
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("kill exact fixture process: %w", err))
		}
		_, _, waitCleanupErr := awaitWindowsTestProcess(cmd.Process, wait, 5*time.Second)
		cleanupErr = errors.Join(cleanupErr, waitCleanupErr)
		if cleanupErr != nil {
			t.Errorf("fixture process cleanup: %v", cleanupErr)
		}
	})
	go func() { wait <- cmd.Wait() }()

	var err error
	owner, err = AttachOwnedProcess(cmd)
	if err != nil {
		t.Fatalf("attach long-lived fixture: %v", err)
	}
	if !waitForWindowsTestFile(ready, 5*time.Second) {
		t.Fatal("long-lived fixture did not report ready")
	}
	running, err := windowsTestProcessRunning(cmd.Process)
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Fatal("long-lived fixture exited before deadline test")
	}

	terminationErr := terminateWindowsOwnedProcess(owner, time.Now().Add(-time.Millisecond))
	waitErr, exited, waitCleanupErr := awaitWindowsTestProcess(cmd.Process, wait, 5*time.Second)
	waitHandled = true
	closeErr := CloseOwnedProcess(owner)
	owner = nil
	if waitCleanupErr != nil || closeErr != nil {
		t.Fatalf("deadline fixture cleanup: wait=%v close=%v", waitCleanupErr, closeErr)
	}
	if !exited {
		t.Fatal("long-lived fixture remained alive after the expired-deadline termination request")
	}
	if waitErr == nil {
		t.Fatal("long-lived fixture reported a successful natural exit after the termination request")
	}
	if terminationErr == nil || !strings.Contains(terminationErr.Error(), "process capture exceeded the cleanup deadline") {
		t.Fatalf("termination error=%v, want retained capture deadline uncertainty", terminationErr)
	}
}

func windowsTestProcessRunning(process *os.Process) (bool, error) {
	var state uint32
	var waitErr error
	if err := process.WithHandle(func(handle uintptr) {
		state, waitErr = windows.WaitForSingleObject(windows.Handle(handle), 0)
	}); err != nil {
		return false, fmt.Errorf("access fixture process handle: %w", err)
	}
	if waitErr != nil {
		return false, fmt.Errorf("inspect fixture process state: %w", waitErr)
	}
	switch state {
	case uint32(windows.WAIT_TIMEOUT):
		return true, nil
	case uint32(windows.WAIT_OBJECT_0):
		return false, nil
	default:
		return false, fmt.Errorf("inspect fixture process state: unexpected wait state 0x%x", state)
	}
}

func awaitWindowsTestProcess(process *os.Process, wait <-chan error, limit time.Duration) (waitErr error, exited bool, cleanupErr error) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case waitErr = <-wait:
		return waitErr, true, nil
	case <-timer.C:
		cleanupErr = errors.New("fixture process did not exit within the first wait bound")
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("fallback kill exact fixture process: %w", err))
	}

	timer.Reset(limit)
	select {
	case waitErr = <-wait:
		return waitErr, true, cleanupErr
	case <-timer.C:
		return nil, false, errors.Join(cleanupErr, errors.New("fixture process did not exit after fallback kill"))
	}
}

func waitForWindowsTestFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
