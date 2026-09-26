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
	noErr(t, cmd.Start())
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
	noErr(t, cleanupErr, "observer failure process cleanup")
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
	noErr(t, cmd.Start())
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
	noErr(t, err, "attach long-lived fixture")
	if !waitForWindowsTestFile(ready, 5*time.Second) {
		t.Fatal("long-lived fixture did not report ready")
	}
	running, err := windowsTestProcessRunning(cmd.Process)
	noErr(t, err)
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

// jobReplay replays what a job reports to cleanup, one entry per query, and
// then repeats the last entry.
type jobReplay struct {
	accounting      []windowsJobAccounting
	lists           [][]uint32
	listErrs        []error
	accountingCalls int
	listCalls       int
}

func (replay *jobReplay) install(t *testing.T) {
	t.Helper()
	originalAccounting, originalList := jobAccountingQuery, jobProcessIDsQuery
	t.Cleanup(func() { jobAccountingQuery, jobProcessIDsQuery = originalAccounting, originalList })
	jobAccountingQuery = func(windows.Handle) (windowsJobAccounting, error) {
		index := min(replay.accountingCalls, len(replay.accounting)-1)
		replay.accountingCalls++
		return replay.accounting[index], nil
	}
	jobProcessIDsQuery = func(windows.Handle, uint32) ([]uint32, error) {
		index := min(replay.listCalls, len(replay.lists)-1)
		replay.listCalls++
		return replay.lists[index], replay.listErrs[index]
	}
}

func jobCounts(total, active uint32) windowsJobAccounting {
	return windowsJobAccounting{totalProcesses: total, activeProcesses: active}
}

// QA-004: a main process that exited and was waited can still be counted, or
// counted but not listed, while cleanup captures the job. These are the forms
// the Windows lab recorded; cleanup must wait for them to settle instead of
// reporting a passing command's cleanup as failed.
func TestTerminateOwnedProcessSettlesExitedProcessDepartures(t *testing.T) {
	listChanged := errors.New("owned job process list changed during capture: assigned=1 listed=0")
	tests := []struct {
		name   string
		replay jobReplay
	}{
		{
			name: "counted but not listed",
			replay: jobReplay{
				accounting: []windowsJobAccounting{jobCounts(1, 1), jobCounts(1, 1), jobCounts(1, 0)},
				lists:      [][]uint32{nil, nil},
				listErrs:   []error{listChanged, nil},
			},
		},
		{
			name: "active count falls during capture",
			replay: jobReplay{
				accounting: []windowsJobAccounting{jobCounts(1, 1), jobCounts(1, 0)},
				lists:      [][]uint32{nil},
				listErrs:   []error{nil},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner, err := newWindowsProcessOwner()
			noErr(t, err, "create job")
			t.Cleanup(func() { noErr(t, CloseOwnedProcess(owner), "close job") })
			replay := test.replay
			replay.install(t)
			if err := terminateWindowsOwnedProcess(owner, time.Now().Add(2*time.Second)); err != nil {
				t.Fatalf("termination error=%v, want departures of an exited process to settle", err)
			}
			if replay.listCalls < 2 {
				t.Fatalf("capture ran %d times, want a retry after the inconsistent capture", replay.listCalls)
			}
		})
	}
}

// Departures are the only inconsistency cleanup waits out: a process that
// joined during capture, or a count that never settles, is still reported.
func TestTerminateOwnedProcessReportsGrowthAndUnsettledCapture(t *testing.T) {
	t.Run("growth", func(t *testing.T) {
		owner, err := newWindowsProcessOwner()
		noErr(t, err, "create job")
		t.Cleanup(func() { noErr(t, CloseOwnedProcess(owner), "close job") })
		replay := jobReplay{
			accounting: []windowsJobAccounting{jobCounts(1, 1), jobCounts(2, 2), jobCounts(2, 0)},
			lists:      [][]uint32{nil},
			listErrs:   []error{nil},
		}
		replay.install(t)
		err = terminateWindowsOwnedProcess(owner, time.Now().Add(2*time.Second))
		if err == nil || !strings.Contains(err.Error(), "membership changed during process capture: total=1/2") {
			t.Fatalf("termination error=%v, want reported membership growth", err)
		}
		// The raw reads and one more read are reported, so the cause of a
		// growth can be told apart later.
		for _, want := range []string{
			"before={total=1 active=1 terminated=0",
			"after={total=2 active=2 terminated=0",
			"later={total=2 active=0 terminated=0",
			"listed=[] retained=[] assigned=0 in job: unknown",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("termination error=%v, want %q", err, want)
			}
		}
		if replay.listCalls != 1 {
			t.Fatalf("capture ran %d times, want growth reported without a retry", replay.listCalls)
		}
	})
	t.Run("never settles", func(t *testing.T) {
		owner, err := newWindowsProcessOwner()
		noErr(t, err, "create job")
		t.Cleanup(func() { noErr(t, CloseOwnedProcess(owner), "close job") })
		replay := jobReplay{
			accounting: []windowsJobAccounting{jobCounts(1, 1)},
			lists:      [][]uint32{nil},
			listErrs:   []error{nil},
		}
		replay.install(t)
		started := time.Now()
		err = terminateWindowsOwnedProcess(owner, started.Add(300*time.Millisecond))
		elapsed := time.Since(started)
		for _, want := range []string{
			"owned job process list is incomplete: unique=0 active=1",
			"owned job still reports 1 active processes after termination",
		} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("termination error=%v, want %q", err, want)
			}
		}
		if replay.listCalls < 2 {
			t.Fatalf("capture ran %d times, want retries before reporting", replay.listCalls)
		}
		if elapsed > 2*time.Second {
			t.Fatalf("termination took %s, want it bounded by the cleanup deadline", elapsed)
		}
	})
}

// startWindowsJobFixture starts a process that stays alive in its own job
// and waits until it runs.
func startWindowsJobFixture(t *testing.T) (*ProcessOwner, *exec.Cmd) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsOwnedProcessFailureFixture$")
	cmd.Env = append(os.Environ(),
		windowsOwnerFailureFixture+"=1",
		windowsOwnerFailureReady+"="+ready,
	)
	ConfigureOwnedProcess(cmd)
	noErr(t, cmd.Start())
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	owner, err := AttachOwnedProcess(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		<-wait
		t.Fatalf("attach fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = TerminateOwnedProcess(owner, time.Second)
		_ = CloseOwnedProcess(owner)
		if _, exited, cleanupErr := awaitWindowsTestProcess(cmd.Process, wait, 5*time.Second); !exited || cleanupErr != nil {
			t.Errorf("fixture cleanup: exited=%v err=%v", exited, cleanupErr)
		}
	})
	// The bound is a hang guard; a loaded machine can start processes slowly.
	if !waitForWindowsTestFile(ready, 30*time.Second) {
		t.Fatal("fixture did not report ready")
	}
	return owner, cmd
}

// When the job's first accounting read comes back empty while its process
// runs, the error shows that the listed process is the one assigned at
// creation, that it is still in the job, and what a later read returns.
func TestTerminateOwnedProcessDescribesAnEmptyAccountingRead(t *testing.T) {
	owner, cmd := startWindowsJobFixture(t)
	originalAccounting := jobAccountingQuery
	t.Cleanup(func() { jobAccountingQuery = originalAccounting })
	calls := 0
	jobAccountingQuery = func(job windows.Handle) (windowsJobAccounting, error) {
		calls++
		if calls == 1 {
			return windowsJobAccounting{}, nil
		}
		return originalAccounting(job)
	}
	err := terminateWindowsOwnedProcess(owner, time.Now().Add(2*time.Second))
	pid := cmd.Process.Pid
	for _, want := range []string{
		"membership changed during process capture: total=0/1 active=0/1",
		"before={total=0 active=0 terminated=0 user=0 kernel=0 faults=0}",
		"after={total=1 active=1",
		"later={total=1 active=1",
		fmt.Sprintf("listed=[%d] retained=[%d] assigned=%d in job: true", pid, pid, pid),
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("termination error=%v, want %q", err, want)
		}
	}
}

// A terminated process can stay counted by its job for a moment after its
// handle is signaled. Cleanup waits for the count to reach zero instead of
// reporting the process as a survivor.
func TestTerminateOwnedProcessWaitsForTerminatedProcessToLeaveTheJob(t *testing.T) {
	owner, _ := startWindowsJobFixture(t)

	// The capture reads the real job twice. The first read after termination
	// still counts the killed process; later reads are real.
	originalAccounting := jobAccountingQuery
	t.Cleanup(func() { jobAccountingQuery = originalAccounting })
	calls := 0
	jobAccountingQuery = func(job windows.Handle) (windowsJobAccounting, error) {
		accounting, err := originalAccounting(job)
		calls++
		if calls == 3 && err == nil && accounting.activeProcesses == 0 {
			accounting.activeProcesses = 1
		}
		return accounting, err
	}
	if err := terminateWindowsOwnedProcess(owner, time.Now().Add(2*time.Second)); err != nil {
		t.Fatalf("termination error=%v, want the lagging count to settle", err)
	}
	if calls < 4 {
		t.Fatalf("job accounting read %d times, want a read after the lagging count", calls)
	}
}
