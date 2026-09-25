//go:build !windows

package githttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// slowArchiveGit makes git archive write 10 KiB and then wait for a child
// process that runs for a minute. Every other Git command runs normally. It
// returns the file that receives the child's process ID.
func slowArchiveGit(t *testing.T, handler *Handler) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	noErr(t, err)
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	wrapper := filepath.Join(root, "git")
	script := "#!/bin/sh\ncase \" $* \" in *\" archive \"*)\n" +
		"  head -c 10240 /dev/zero\n" +
		"  sleep 60 &\n  child=$!\n  printf '%s' \"$child\" > " + quoteShell(pidFile) + "\n  wait \"$child\"\n  exit 0;;\nesac\n" +
		"exec " + quoteShell(realGit) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	handler.Git.GitPath = wrapper
	handler.Git.TerminationGrace = 25 * time.Millisecond
	previous := archiveHoldback
	archiveHoldback = 1 << 10
	t.Cleanup(func() { archiveHoldback = previous })
	return pidFile
}

func waitForPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if content, err := os.ReadFile(pidFile); err == nil {
			if pid, _ := strconv.Atoi(strings.TrimSpace(string(content))); pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("git archive did not start its child process")
	return 0
}

func requireProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("git archive child process %d remained", pid)
}

// A client that goes away stops Git and its children before the handler
// returns.
func TestArchiveCancellationStopsGit(t *testing.T) {
	handler, commitOID := archiveFixture(t, 16)
	pidFile := slowArchiveGit(t, handler)
	server := serveArchiveOf(t, handler, commitOID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"?format=zip", nil)
	noErr(t, err)
	response, err := http.DefaultClient.Do(request)
	noErr(t, err)
	defer response.Body.Close()
	pid := waitForPID(t, pidFile)
	cancel()
	requireProcessGone(t, pid)
	waitContext, waitCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer waitCancel()
	noErr(t, handler.Wait(waitContext), "wait for the archive operation")
	if handler.Active() != 0 {
		t.Fatalf("%d archive operations still active", handler.Active())
	}
}

// Git that runs past the operation timeout is stopped, and the part already
// sent ends as a failed transfer.
func TestArchiveTimeoutStopsGitAndFailsTheTransfer(t *testing.T) {
	handler, commitOID := archiveFixture(t, 16)
	pidFile := slowArchiveGit(t, handler)
	handler.OperationTimeout = 500 * time.Millisecond
	logs := captureLog(t)
	server := serveArchiveOf(t, handler, commitOID)
	response, err := http.Get(server.URL + "?format=zip")
	noErr(t, err)
	defer response.Body.Close()
	pid := waitForPID(t, pidFile)
	body, err := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || err == nil || len(body) == 0 {
		t.Fatalf("status=%d received=%d read error=%v, want a started transfer that fails", response.StatusCode, len(body), err)
	}
	requireProcessGone(t, pid)
	if !strings.Contains(logs.String(), `Git archive request for repository "sample" failed: operation timed out`) {
		t.Fatalf("log=%q", logs.String())
	}
}

// Git that writes a whole archive and then fails does not deliver a valid
// archive: the held-back tail with the end records is never sent.
func TestArchiveFailingAfterItsLastByteSendsNoValidArchive(t *testing.T) {
	handler, commitOID := archiveFixture(t, 200<<10)
	realGit, err := exec.LookPath("git")
	noErr(t, err)
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\ncase \" $* \" in *\" archive \"*)\n  " + quoteShell(realGit) + " \"$@\"\n  exit 1;;\nesac\n" +
		"exec " + quoteShell(realGit) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	handler.Git.GitPath = wrapper
	captureLog(t)
	server := serveArchiveOf(t, handler, commitOID)
	for format, read := range map[string]func([]byte) (map[string]string, error){ArchiveZip: zipEntries, ArchiveTarGz: tarGzEntries} {
		response, body, err := fetchArchive(t, server.URL+"?format="+format)
		if response.StatusCode != http.StatusOK || err == nil || len(body) == 0 {
			t.Fatalf("%s: status=%d received=%d read error=%v", format, response.StatusCode, len(body), err)
		}
		if entries, err := read(body); err == nil {
			t.Fatalf("%s: a failed archive reads as complete: %s", format, entryNames(entries))
		}
	}
}

func TestArchiveFailureReasonNamesASignal(t *testing.T) {
	err := exec.Command("/bin/sh", "-c", "kill -9 $$").Run()
	if got := archiveFailureReason(err, time.Time{}); got != "git archive was stopped by a signal" {
		t.Fatalf("killed Git: %q", got)
	}
	err = exec.Command("/bin/sh", "-c", "exit 3").Run()
	if got := archiveFailureReason(err, time.Time{}); got != "git archive exited with status 3" {
		t.Fatalf("failed Git: %q", got)
	}
	if got := archiveFailureReason(context.Canceled, time.Time{}); got != "" {
		t.Fatalf("client gone: %q", got)
	}
}
