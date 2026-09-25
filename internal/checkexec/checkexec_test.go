package checkexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
)

func TestRunReportsRealOutcomes(t *testing.T) {
	results, cancelled := Run(context.Background(), []Definition{
		{Name: "pass", Command: "echo hello"},
		{Name: "fail", Command: "exit 3"},
		{Name: "fail-one", Command: "exit 1"},
		{Name: "missing", Command: "definitely-not-a-command-owngit"},
	}, Options{Timeout: 30 * time.Second})
	if cancelled {
		t.Fatal("run was unexpectedly cancelled")
	}
	if len(results) != 4 {
		t.Fatalf("results=%d", len(results))
	}
	if results[0].Status != StatusPassed || !strings.Contains(results[0].Output, "hello") {
		t.Fatalf("passing result=%+v", results[0])
	}
	if results[1].Status != StatusFailed || results[1].ExitCode == nil || *results[1].ExitCode != 3 {
		t.Fatalf("failing result=%+v", results[1])
	}
	if results[2].Status != StatusFailed || results[2].ExitCode == nil || *results[2].ExitCode != 1 {
		t.Fatalf("exit-one result=%+v", results[2])
	}
	missing := results[3]
	if runtime.GOOS == "windows" {
		if missing.Status != StatusFailed || missing.ExitCode == nil || *missing.ExitCode != 1 {
			t.Fatalf("Windows missing command result=%+v exit=%v", missing, exitCodeValue(missing.ExitCode))
		}
	} else if missing.Status != StatusUnavailable || missing.ExitCode == nil || *missing.ExitCode != 127 {
		t.Fatalf("missing command result=%+v exit=%v", missing, exitCodeValue(missing.ExitCode))
	}
}

func TestRunBoundsOutputAndRedactsSecrets(t *testing.T) {
	long := strings.Repeat("x", 200)
	results, _ := Run(context.Background(), []Definition{
		{Name: "long", Command: "echo " + long},
		{Name: "secret", Command: "echo secret-token"},
	}, Options{Timeout: 30 * time.Second, OutputLimit: 16, Redact: []string{"secret-token"}})
	if results[0].Status != StatusIncomplete || !results[0].Truncated || len(results[0].Output) > 16 {
		t.Fatalf("truncated result=%+v", results[0])
	}
	if strings.Contains(results[1].Output, "secret-token") || !strings.Contains(results[1].Output, "[redacted]") {
		t.Fatalf("redacted output=%q", results[1].Output)
	}
}

func TestRunCancellationTerminatesTheOwnedProcess(t *testing.T) {
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "ping -n 30 127.0.0.1"
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var results []Result
	var cancelled bool
	go func() {
		results, cancelled = Run(ctx, []Definition{{Name: "slow", Command: command}}, Options{Timeout: time.Minute})
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("cancelled run did not finish")
	}
	if !cancelled || len(results) != 1 || results[0].Status != StatusCancelled {
		t.Fatalf("cancelled=%v results=%+v", cancelled, results)
	}
}

func TestRunTimeoutIsIncompleteNotFailed(t *testing.T) {
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "ping -n 30 127.0.0.1"
	}
	results, cancelled := Run(context.Background(), []Definition{{Name: "slow", Command: command}}, Options{Timeout: 300 * time.Millisecond})
	if cancelled || len(results) != 1 || results[0].Status != StatusIncomplete {
		t.Fatalf("timeout results=%+v cancelled=%v", results, cancelled)
	}
}

func TestRunBoundsWaitWhenTerminationFails(t *testing.T) {
	originalObserver := ownedProcessStartedObserver
	originalTerminate := terminateOwnedProcess
	originalClose := closeOwnedProcess
	originalWaitLimit := cleanupWaitLimit
	cleanupWaitLimit = 25 * time.Millisecond

	var capturedOwner, closedOwner *gitexec.ProcessOwner
	terminationCalls := 0
	terminateOwnedProcess = func(owner *gitexec.ProcessOwner, _ time.Duration) error {
		capturedOwner = owner
		terminationCalls++
		return errors.New("forced termination failure")
	}
	closeOwnedProcess = func(owner *gitexec.ProcessOwner) error {
		closedOwner = owner
		return nil
	}
	cleaned := false
	t.Cleanup(func() {
		ownedProcessStartedObserver = originalObserver
		terminateOwnedProcess = originalTerminate
		closeOwnedProcess = originalClose
		cleanupWaitLimit = originalWaitLimit
		if !cleaned && capturedOwner != nil {
			if err := errors.Join(
				originalTerminate(capturedOwner, 2*time.Second),
				originalClose(capturedOwner),
			); err != nil {
				t.Errorf("clean up captured owner after test failure: %v", err)
			}
		}
	})

	command := "ping -n 3 127.0.0.1"
	var childPID int
	var leakMarker string
	if runtime.GOOS != "windows" {
		directory := t.TempDir()
		childPIDPath := filepath.Join(directory, "child.pid")
		leakMarker = filepath.Join(directory, "leaked")
		quote := func(value string) string {
			return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
		}
		command = fmt.Sprintf(
			"(sleep 1; printf leaked > %s) & child=$!; printf '%%s' \"$child\" > %s; wait \"$child\"",
			quote(leakMarker), quote(childPIDPath),
		)
		ownedProcessStartedObserver = func() error {
			deadline := time.Now().Add(300 * time.Millisecond)
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(childPIDPath)
				if err == nil {
					pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
					if parseErr == nil && pid > 0 {
						childPID = pid
						return nil
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
			return errors.New("owned background child did not report its PID")
		}
	}

	started := time.Now()
	results, cancelled := Run(context.Background(), []Definition{{Name: "slow", Command: command}}, Options{Timeout: 50 * time.Millisecond})
	elapsed := time.Since(started)

	ownedProcessStartedObserver = originalObserver
	terminateOwnedProcess = originalTerminate
	closeOwnedProcess = originalClose
	cleanupWaitLimit = originalWaitLimit
	if capturedOwner == nil || closedOwner != capturedOwner {
		t.Fatalf("captured owner=%v closed owner=%v", capturedOwner, closedOwner)
	}
	cleanupErr := errors.Join(
		originalTerminate(capturedOwner, 2*time.Second),
		originalClose(capturedOwner),
	)
	if cleanupErr != nil {
		t.Fatalf("clean up captured owner: %v", cleanupErr)
	}
	cleaned = true

	if elapsed > 750*time.Millisecond {
		t.Fatalf("termination failure left Run waiting for %v", elapsed)
	}
	if cancelled || len(results) != 1 || results[0].Status != StatusError {
		t.Fatalf("timeout results=%+v cancelled=%v", results, cancelled)
	}
	if terminationCalls != 2 || strings.Count(results[0].CleanupError, "forced termination failure") != 2 {
		t.Fatalf("termination calls=%d result=%+v", terminationCalls, results[0])
	}
	t.Logf("Run returned in %v after %d injected termination failures; cleanup=%q", elapsed, terminationCalls, results[0].CleanupError)
	if runtime.GOOS != "windows" {
		if childPID <= 0 {
			t.Fatal("owned background child PID was not captured")
		}
		for _, message := range []string{
			"process did not exit after owned-process termination",
			"process wait remained blocked after cleanup",
		} {
			if !strings.Contains(results[0].CleanupError, message) {
				t.Fatalf("cleanup error %q does not contain %q", results[0].CleanupError, message)
			}
		}
		time.Sleep(time.Until(started.Add(1200 * time.Millisecond)))
		if _, err := os.Stat(leakMarker); !os.IsNotExist(err) {
			t.Fatalf("owned background child %d survived cleanup: %v", childPID, err)
		}
	}
}

// TestRunReportsCleanupFailureOnEveryPath forces the first owner termination
// to fail before the real retry runs.
func TestRunReportsCleanupFailureOnEveryPath(t *testing.T) {
	originalTerminate := terminateOwnedProcess
	t.Cleanup(func() { terminateOwnedProcess = originalTerminate })
	slow := "sleep 30"
	if runtime.GOOS == "windows" {
		slow = "ping -n 30 127.0.0.1"
	}
	cases := []struct {
		name        string
		command     string
		timeout     time.Duration
		cancelAfter time.Duration
	}{
		{name: "success", command: "echo ok"},
		{name: "failure", command: "exit 7"},
		{name: "missing", command: "definitely-not-a-command-owngit"},
		{name: "timeout", command: slow, timeout: 300 * time.Millisecond},
		{name: "cancelled", command: slow, cancelAfter: 200 * time.Millisecond},
	}
	for _, testCase := range cases {
		failFirstTermination := true
		terminateOwnedProcess = func(owner *gitexec.ProcessOwner, grace time.Duration) error {
			if failFirstTermination {
				failFirstTermination = false
				return errors.New("forced first termination failure")
			}
			return originalTerminate(owner, grace)
		}
		ctx := context.Background()
		if testCase.cancelAfter > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			go func() {
				time.Sleep(testCase.cancelAfter)
				cancel()
			}()
			defer cancel()
		}
		results, cancelled := Run(ctx, []Definition{{Name: testCase.name, Command: testCase.command}}, Options{Timeout: testCase.timeout})
		if cancelled != (testCase.name == "cancelled") {
			t.Fatalf("%s cancelled=%v", testCase.name, cancelled)
		}
		result := results[0]
		if result.CleanupError == "" {
			t.Fatalf("%s did not report the cleanup failure: %+v", testCase.name, result)
		}
		if result.Status != StatusError {
			t.Fatalf("%s status=%q want error", testCase.name, result.Status)
		}
		switch testCase.name {
		case "success":
			if result.ExitCode == nil || *result.ExitCode != 0 {
				t.Fatalf("success exit code=%v want 0", result.ExitCode)
			}
		case "failure":
			if result.ExitCode == nil || *result.ExitCode != 7 {
				t.Fatalf("failure exit code=%v want 7", result.ExitCode)
			}
		case "missing":
			want := 127
			if runtime.GOOS == "windows" {
				want = 1
			}
			if result.ExitCode == nil || *result.ExitCode != want {
				t.Fatalf("missing exit code=%v want=%d output=%q", exitCodeValue(result.ExitCode), want, result.Output)
			}
		}
	}
}

// QA-004: after a command exits and is waited, its main process is gone and
// Windows reports EINVAL for a kill. Cleanup must not attempt it or report its
// failure; only the owner termination failure explains the error.
func TestRunDoesNotKillAWaitedMainProcess(t *testing.T) {
	originalTerminate, originalKill := terminateOwnedProcess, killMainProcess
	t.Cleanup(func() { terminateOwnedProcess, killMainProcess = originalTerminate, originalKill })
	failFirstTermination := true
	terminateOwnedProcess = func(owner *gitexec.ProcessOwner, grace time.Duration) error {
		if failFirstTermination {
			failFirstTermination = false
			return errors.New("forced first termination failure")
		}
		return originalTerminate(owner, grace)
	}
	kills := 0
	killMainProcess = func(*os.Process) error {
		kills++
		return errors.New("kill main process: invalid argument")
	}

	results, cancelled := Run(context.Background(), []Definition{{Name: "success", Command: "echo ok"}}, Options{Timeout: 10 * time.Second})
	result := results[0]
	if cancelled || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result=%+v cancelled=%v, want a normal exit", result, cancelled)
	}
	if kills != 0 || strings.Contains(result.CleanupError, "kill main process") {
		t.Fatalf("kills=%d cleanup=%q, want no kill of the waited main process", kills, result.CleanupError)
	}
	if !strings.Contains(result.CleanupError, "forced first termination failure") || result.Status != StatusError {
		t.Fatalf("result=%+v, want the termination failure reported", result)
	}
}

func exitCodeValue(code *int) any {
	if code == nil {
		return nil
	}
	return *code
}

func TestRunReportsAttachmentFailureWhenFallbackSucceeds(t *testing.T) {
	originalAttach := attachOwnedProcess
	defer func() { attachOwnedProcess = originalAttach }()
	attachOwnedProcess = func(*exec.Cmd) (*gitexec.ProcessOwner, error) {
		return nil, errors.New("forced attachment failure")
	}

	command := "exec sleep 2"
	if runtime.GOOS == "windows" {
		command = "ping -n 3 127.0.0.1"
	}
	started := time.Now()
	results, cancelled := Run(context.Background(), []Definition{{Name: "attach", Command: command}}, Options{Timeout: time.Second})
	if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
		t.Fatalf("attachment fallback took %v", elapsed)
	}
	result := results[0]
	if cancelled || result.Status != StatusError || result.ExitCode == nil {
		t.Fatalf("attachment result=%+v cancelled=%v", result, cancelled)
	}
	if !strings.Contains(result.Output, "forced attachment failure") || !strings.Contains(result.CleanupError, "forced attachment failure") {
		t.Fatalf("attachment result=%+v", result)
	}
}

func TestRunPreservesAttachmentCleanupFailures(t *testing.T) {
	originalAttach, originalKill, originalWait := attachOwnedProcess, killMainProcess, cleanupWaitLimit
	defer func() {
		attachOwnedProcess, killMainProcess, cleanupWaitLimit = originalAttach, originalKill, originalWait
	}()
	attachOwnedProcess = func(*exec.Cmd) (*gitexec.ProcessOwner, error) {
		return nil, errors.New("forced attachment failure")
	}
	firstKill := true
	killMainProcess = func(process *os.Process) error {
		if firstKill {
			firstKill = false
			return errors.New("forced fallback failure")
		}
		return originalKill(process)
	}
	cleanupWaitLimit = 25 * time.Millisecond

	command := "exec sleep 2"
	if runtime.GOOS == "windows" {
		command = "ping -n 3 127.0.0.1"
	}
	started := time.Now()
	results, _ := Run(context.Background(), []Definition{{Name: "attach", Command: command}}, Options{Timeout: time.Second})
	if elapsed := time.Since(started); elapsed > 750*time.Millisecond {
		t.Fatalf("attachment cleanup took %v", elapsed)
	}
	result := results[0]
	if result.Status != StatusError || !strings.Contains(result.Output, "forced attachment failure") || !strings.Contains(result.CleanupError, "forced fallback failure") {
		t.Fatalf("attachment result=%+v", result)
	}
}

func TestRunBoundsAndPreservesAllTerminationFailures(t *testing.T) {
	originalTerminate, originalClose := terminateOwnedProcess, closeOwnedProcess
	originalKill, originalWait := killMainProcess, cleanupWaitLimit
	var capturedOwner *gitexec.ProcessOwner
	var capturedProcess *os.Process
	terminateOwnedProcess = func(owner *gitexec.ProcessOwner, _ time.Duration) error {
		capturedOwner = owner
		return errors.New("forced owner termination failure")
	}
	killMainProcess = func(process *os.Process) error {
		capturedProcess = process
		return errors.New("forced fallback termination failure")
	}
	closeOwnedProcess = func(owner *gitexec.ProcessOwner) error {
		capturedOwner = owner
		return errors.New("forced owner close failure")
	}
	cleanupWaitLimit = 25 * time.Millisecond

	command := "exec sleep 2"
	if runtime.GOOS == "windows" {
		command = "ping -n 3 127.0.0.1"
	}
	started := time.Now()
	results, _ := Run(context.Background(), []Definition{{Name: "bounded", Command: command}}, Options{Timeout: 25 * time.Millisecond})
	elapsed := time.Since(started)

	terminateOwnedProcess, closeOwnedProcess = originalTerminate, originalClose
	killMainProcess, cleanupWaitLimit = originalKill, originalWait
	if capturedOwner != nil {
		_ = originalTerminate(capturedOwner, 2*time.Second)
		_ = originalClose(capturedOwner)
	}
	if capturedProcess != nil {
		_ = originalKill(capturedProcess)
	}

	if elapsed > 750*time.Millisecond {
		t.Fatalf("failed cleanup remained blocked for %v", elapsed)
	}
	result := results[0]
	for _, text := range []string{
		"forced owner termination failure",
		"forced fallback termination failure",
		"forced owner close failure",
		"process wait remained blocked",
	} {
		if !strings.Contains(result.CleanupError, text) {
			t.Fatalf("cleanup error %q does not contain %q", result.CleanupError, text)
		}
	}
	if result.Status != StatusError {
		t.Fatalf("result=%+v", result)
	}
}
