//go:build !windows

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// wrapMaintenanceGit makes the manager run Git through a script that, for
// repack, does what the mode file says: "hang" records its PID and sleeps,
// "fail" reports a synthetic storage error. Other commands run real Git.
func wrapMaintenanceGit(t *testing.T, manager *Manager) (setMode func(string), pidFile string) {
	t.Helper()
	directory := t.TempDir()
	modeFile := filepath.Join(directory, "mode")
	pidFile = filepath.Join(directory, "pid")
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *" repack "*)
    case "$(cat %[1]q 2>/dev/null)" in
      hang) echo $$ >%[2]q; exec sleep 60 ;;
      fail) echo "fatal: synthetic: No space left on device" >&2; exit 128 ;;
    esac ;;
esac
exec %[3]q "$@"
`, modeFile, pidFile, manager.Git.GitPath)
	wrapper := filepath.Join(directory, "git")
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	wrapped := *manager.Git
	wrapped.GitPath = wrapper
	manager.Git = &wrapped
	return func(mode string) { noErr(t, os.WriteFile(modeFile, []byte(mode), 0o600)) }, pidFile
}

func waitForPID(t *testing.T, pidFile string) int {
	t.Helper()
	var pid int
	waitFor(t, "the hanging repack", func() bool {
		content, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(content)))
		return err == nil && pid > 0
	})
	return pid
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("maintenance process %d is still running (kill 0: %v)", pid, err)
	}
}

// assertRepositoryUsable checks that no lock file was left behind, that the
// repository is consistent, and that a push with retention still works.
func assertRepositoryUsable(t *testing.T, fixture *maintenanceFixture) {
	t.Helper()
	noErr(t, filepath.WalkDir(fixture.remote, func(path string, entry os.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".lock") {
			t.Fatalf("lock file left behind: %s", path)
		}
		return err
	}))
	runGit(t, "", "--git-dir", fixture.remote, "fsck", "--no-dangling")
	replaced := gitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main")
	parent := gitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main~1")
	runGit(t, fixture.work, "push", "--force", "origin", parent+":refs/heads/main")
	assertRef(t, fixture.remote, "refs/owngit/retained/heads/"+replaced, replaced)
}

func TestStopMaintenanceTerminatesItsGitProcess(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	before := inventory(t, fixture.remote)
	setMode, pidFile := wrapMaintenanceGit(t, manager)
	setMode("hang")
	log := &maintenanceLog{}
	noErr(t, manager.StartMaintenance(context.Background(), MaintenanceSchedule{Idle: time.Millisecond, Now: shiftedClock(12)}, log.logf))
	manager.NoteRepositoryWrite("sample")
	pid := waitForPID(t, pidFile)

	stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	begun := time.Now()
	noErr(t, manager.StopMaintenance(stop))
	if elapsed := time.Since(begun); elapsed > 5*time.Second {
		t.Fatalf("stop took %s", elapsed)
	}
	assertProcessGone(t, pid)
	if lines := log.matching(`"sample" maintenance (small) stopped`); len(lines) != 1 {
		t.Fatalf("log: %v", log.matching("maintenance"))
	}
	assertSameInventory(t, before, inventory(t, fixture.remote))
	assertRepositoryUsable(t, fixture)
}

func TestMaintenanceTimeoutAndFailureLeaveTheRepositoryUsable(t *testing.T) {
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	before := inventory(t, fixture.remote)
	setMode, pidFile := wrapMaintenanceGit(t, manager)
	setMode("hang")
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{
		Idle: time.Millisecond, Retry: time.Millisecond, FailureRetry: 300 * time.Millisecond,
		CommandTimeout: 300 * time.Millisecond, Now: shiftedClock(12),
	})
	manager.NoteRepositoryWrite("sample")
	pid := waitForPID(t, pidFile)
	waitFor(t, "timeout", func() bool { return len(log.matching("failed")) == 1 })
	assertProcessGone(t, pid)
	if line := log.matching("failed")[0]; !strings.Contains(line, "1 of 3 steps") {
		t.Fatalf("timeout log: %s", line)
	}
	if !manager.pendingMaintenance("sample") {
		t.Fatal("timed-out maintenance is not retried")
	}
	assertSameInventory(t, before, inventory(t, fixture.remote))

	setMode("fail")
	waitFor(t, "failure", func() bool { return len(log.matching("No space left on device")) == 1 })
	assertSameInventory(t, before, inventory(t, fixture.remote))

	setMode("")
	waitFor(t, "recovery", func() bool { return len(log.matching(`"sample" maintenance (small) completed`)) == 1 })
	assertSameInventory(t, before, inventory(t, fixture.remote))
	assertRepositoryUsable(t, fixture)
}
