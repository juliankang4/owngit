//go:build !windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"owngit/internal/checkapi"
)

// commitSlowCheck commits a check that writes its shell's process ID to
// markers/pid, sleeps, and would then write markers/done. The markers live
// outside the work tree so they do not make it dirty.
func commitSlowCheck(t *testing.T, work string) (markers string) {
	t.Helper()
	markers = t.TempDir()
	command := "echo $$ > " + filepath.Join(markers, "pid") + "; sleep 30; echo done > " + filepath.Join(markers, "done")
	encoded, err := json.Marshal(command)
	noErr(t, err)
	writeCommittedChecks(t, work, `{"version":1,"events":{"push":{}},"checks":[{"name":"slow","command":`+string(encoded)+`}]}`)
	return markers
}

// awaitCheckStarted returns the process ID of the running check's shell.
func awaitCheckStarted(t *testing.T, markers string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		content, err := os.ReadFile(filepath.Join(markers, "pid"))
		if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(content))); err == nil && parseErr == nil {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("the check did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitCancelledAttempt waits until the task's latest attempt is recorded as
// cancelled, and checks that the check's process is gone and never finished.
func awaitCancelledAttempt(t *testing.T, remoteFlags []string, taskID, markers string, pid int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var status checkapi.TaskResponse
		noErr(t, json.Unmarshal([]byte(cliOutput(t, checkCommand, append([]string{"status", "--task", taskID}, remoteFlags...)...)), &status))
		if status.Attempt != nil && status.Attempt.Status == "cancelled" {
			if status.Attempt.WorktreeState != "clean" {
				t.Fatalf("cancelled attempt worktree %q", status.Attempt.WorktreeState)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the attempt was not recorded as cancelled: %+v", status.Attempt)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the check's process %d is still there: %v", pid, err)
	}
	if _, err := os.Stat(filepath.Join(markers, "done")); err == nil {
		t.Fatal("the check ran to its end")
	}
}

// A cancellation of check_run stops the checks, still records the attempt as
// cancelled, and gets no response; a second run during the first is refused.
func TestMCPCancellationStopsARunningCheck(t *testing.T) {
	remoteFlags, taskID, work := startMCPCheckFixture(t)
	markers := commitSlowCheck(t, work)
	session := startMCPSession(t, mcpOptions{server: remoteFlags[1], repository: "project", credentialFile: remoteFlags[6], acceptInsecureHTTP: true, workdir: work})
	session.send(`{"jsonrpc":"2.0","id":"run","method":"tools/call","params":{"name":"check_run","arguments":{"task":"` + taskID + `"}}}`)
	pid := awaitCheckStarted(t, markers)
	if code := session.callError("check_run", map[string]any{"task": taskID}); code != "check_run_busy" {
		t.Fatalf("a second run during the first: %q", code)
	}
	session.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"run"}}`)
	awaitCancelledAttempt(t, remoteFlags, taskID, markers, pid)
	session.expectSilence()
}

// The end of the input during check_run stops the checks and records the
// attempt before the session ends, in process and in the built binary.
func TestMCPEndOfInputStopsARunningCheck(t *testing.T) {
	remoteFlags, taskID, work := startMCPCheckFixture(t)
	markers := commitSlowCheck(t, work)
	session := startMCPSession(t, mcpOptions{server: remoteFlags[1], repository: "project", credentialFile: remoteFlags[6], acceptInsecureHTTP: true, workdir: work})
	session.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_run","arguments":{"task":"` + taskID + `"}}}`)
	pid := awaitCheckStarted(t, markers)
	noErr(t, session.input.Close())
	select {
	case err := <-session.done:
		noErr(t, err)
		session.done <- nil // for the cleanup
	case <-time.After(30 * time.Second):
		t.Fatal("the session did not end")
	}
	awaitCancelledAttempt(t, remoteFlags, taskID, markers, pid)

	noErr(t, os.Remove(filepath.Join(markers, "pid")))
	binary := buildOwngit(t)
	binarySession, command, stderr := startMCPBinary(t, binary, work,
		"--server", remoteFlags[1], "--repository", "project", "--credential-file", remoteFlags[6], "--accept-insecure-http")
	binarySession.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"check_run","arguments":{"task":"` + taskID + `"}}}`)
	pid = awaitCheckStarted(t, markers)
	noErr(t, binarySession.input.Close())
	select {
	case err := <-binarySession.done:
		if err != nil {
			t.Fatalf("owngit mcp exited with %v; stderr:\n%s", err, stderr.String())
		}
		binarySession.done <- nil
	case <-time.After(30 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("owngit mcp did not exit")
	}
	awaitCancelledAttempt(t, remoteFlags, taskID, markers, pid)
}

// Inspecting the worktree for a check run does not start the clone's
// core.fsmonitor program.
func TestCheckRunDoesNotRunTheClonesFileSystemMonitor(t *testing.T) {
	_, taskID, work := startMCPCheckFixture(t)
	markers := t.TempDir()
	monitor := writePrivate(t, filepath.Join(markers, "monitor"), "#!/bin/sh\necho ran >> "+filepath.Join(markers, "monitor-ran")+"\nexit 1\n")
	noErr(t, os.Chmod(monitor, 0o700))
	runPRGit(t, work, "config", "core.fsmonitor", monitor)
	output := cliOutput(t, checkCommand, "run", "--no-upload", "--task", taskID, "--workdir", work, "--check", "pass=exit 0")
	var result checkRunOutput
	noErr(t, json.Unmarshal([]byte(output), &result))
	if result.Attempt == nil || result.Attempt.Status != "passed" || result.Attempt.WorktreeState != "clean" {
		t.Fatalf("check run: %s", output)
	}
	if _, err := os.Stat(filepath.Join(markers, "monitor-ran")); err == nil {
		t.Fatal("the clone's core.fsmonitor program ran")
	}
	// The monitor is off only for OwnGit's inspection, not for the user.
	runPRGit(t, work, "status", "--porcelain")
	if _, err := os.Stat(filepath.Join(markers, "monitor-ran")); err != nil {
		t.Fatalf("the fixture's monitor does not run with Git itself: %v", err)
	}
}
