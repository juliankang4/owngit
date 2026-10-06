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
// repack, does what the mode file says: "hang" adds its PID as a line to the
// PID file and sleeps, "fail" reports a synthetic storage error. Other
// commands run real Git. Attempts run one at a time, so line n is the PID of
// the nth hanging repack.
func wrapMaintenanceGit(t *testing.T, manager *Manager) (setMode func(string), pidFile string) {
	t.Helper()
	directory := t.TempDir()
	modeFile := filepath.Join(directory, "mode")
	pidFile = filepath.Join(directory, "pid")
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *" repack "*)
    case "$(cat %[1]q 2>/dev/null)" in
      hang) echo $$ >>%[2]q; exec sleep 60 ;;
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

// waitForPIDs waits until count hanging repacks have started and returns
// their PIDs in the order they started.
func waitForPIDs(t *testing.T, pidFile string, count int) []int {
	t.Helper()
	var pids []int
	waitFor(t, fmt.Sprintf("hanging repack %d", count), func() bool {
		pids = startedPIDs(t, pidFile)
		return len(pids) >= count
	})
	return pids
}

// startedPIDs returns the PIDs of the complete lines of the PID file.
func startedPIDs(t *testing.T, pidFile string) []int {
	t.Helper()
	content, err := os.ReadFile(pidFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	noErr(t, err)
	lines := strings.Split(string(content), "\n")
	var pids []int
	for _, line := range lines[:len(lines)-1] {
		pid, err := strconv.Atoi(line)
		noErr(t, err)
		pids = append(pids, pid)
	}
	return pids
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
	pid := waitForPIDs(t, pidFile, 1)[0]

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
	if testing.Short() {
		t.Skip("runs real repacks that time out and retry")
	}
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
	// The other steps run real Git under the same short limit, so on a busy
	// machine an earlier step can time out first and the hang starts in a
	// later attempt. The repack's own timeout is the failure after one step,
	// and the first such failure belongs to the first hanging repack. A
	// timed-out maintenance is retried, so later repacks may already run.
	waitFor(t, "timeout", func() bool {
		for _, line := range log.matching("failed") {
			if strings.Contains(line, "1 of 3 steps") {
				return true
			}
		}
		return false
	})
	assertProcessGone(t, waitForPIDs(t, pidFile, 1)[0])
	// The retry of the timed-out maintenance hangs again in its repack.
	waitForPIDs(t, pidFile, 2)
	assertSameInventory(t, before, inventory(t, fixture.remote))

	// A failed maintenance is logged with Git's error, retried, and leaves the
	// repository as it was. The retries in each mode run on their own clock,
	// so the stages below wait for the first matching line, not for a count.
	setMode("fail")
	waitFor(t, "failure", func() bool { return len(log.matching("No space left on device")) > 0 })
	assertSameInventory(t, before, inventory(t, fixture.remote))

	setMode("")
	waitFor(t, "recovery", func() bool { return len(log.matching(`"sample" maintenance (small) completed`)) > 0 })
	assertSameInventory(t, before, inventory(t, fixture.remote))
	assertRepositoryUsable(t, fixture)
}

// A pack folder that cannot be read is an error, not a repository without
// work: the night is reported as failed and recorded, so the next attempt is
// the next night and the failure is never silent.
func TestMaintenanceReportsAnUnreadablePackFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires Unix read-permission enforcement for this account")
	}
	fixture := newMaintenanceFixture(t)
	manager := fixture.manager
	// The maintained repository has no packable work, so only the nightly
	// pack inventory can speak about it.
	_, err := manager.maintain(context.Background(), "sample", MaintenanceFull, MaintenanceSchedule{}.withDefaults(), false)
	noErr(t, err)
	pack := filepath.Join(fixture.remote, "objects", "pack")
	noErr(t, os.Chmod(pack, 0o000))
	t.Cleanup(func() { _ = os.Chmod(pack, 0o700) })
	log := startMaintenanceForTest(t, manager, MaintenanceSchedule{Idle: time.Millisecond, Retry: time.Millisecond, Now: shiftedClock(3)})
	waitFor(t, "the pack inventory error", func() bool { return len(log.matching(`"sample" maintenance (small) failed`)) > 0 })
	if lines := log.matching("objects/pack"); len(lines) != 1 {
		t.Fatalf("the failure did not name the pack folder: %v", log.matching("maintenance"))
	}
	if lines := log.matching("completed"); len(lines) != 0 {
		t.Fatalf("maintenance reported a completed run: %v", lines)
	}
}
