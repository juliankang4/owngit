package backups

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"owngit/internal/recovery"
	"owngit/internal/state"
)

func TestFailedServingBackupHasNoAliasNotice(t *testing.T) {
	f := newFixture(t)
	remote, err := f.manager.Path("project")
	noErr(t, err)
	git(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	f.configure(t, ScheduleChange{})
	var logs []string
	f.service.Logf = func(format string, arguments ...any) { logs = append(logs, fmt.Sprintf(format, arguments...)) }
	f.service.VerifyLimit = time.Nanosecond
	run := f.backUpNow(t)
	if run.Status != state.BackupFailed || !strings.Contains(run.Message, "did not finish within 1ns") || strings.Contains(run.Message, recovery.AliasBranchNotice) || strings.Contains(strings.Join(logs, "\n"), "alias notice") {
		t.Fatalf("failed backup gained alias guidance or lost its error: %+v, logs %v", run, logs)
	}
}

func TestServingBackupKeepsMissingAliasNotice(t *testing.T) {
	f := newFixture(t)
	remote, err := f.manager.Path("project")
	noErr(t, err)
	git(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/missing-target", "refs/heads/future")
	git(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/chained-missing", "refs/heads/missing-target")
	f.configure(t, ScheduleChange{})
	run := f.backUpNow(t)
	if run.Status != state.BackupSucceeded || run.Verification != state.BackupVerifyPassed || !strings.Contains(run.Message, recovery.MissingAliasBranchNotice) || strings.Contains(run.Message, recovery.AliasBranchNotice) {
		t.Fatalf("serving backup failed or misstated unresolved aliases: %+v", run)
	}
}

func TestInterruptedServingBackupHasNoAliasNotice(t *testing.T) {
	f := newFixture(t)
	remote, err := f.manager.Path("project")
	noErr(t, err)
	git(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	schedule := f.configure(t, ScheduleChange{})
	run := state.BackupRun{ID: strings.Repeat("d", 32), Kind: state.BackupRunManual, Status: state.BackupRunning,
		Destination: f.destination, BackupName: namePrefix + "interrupted-alias", Verification: state.BackupVerifyNotRun, StartedAt: f.clock.Now()}
	noErr(t, f.store.StartBackupRun(context.Background(), run))
	var logs []string
	f.service.Logf = func(format string, arguments ...any) { logs = append(logs, fmt.Sprintf(format, arguments...)) }
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	f.service.execute(stopped, run, schedule)
	run = f.run(t, run.ID)
	if run.Status != state.BackupInterrupted || run.Message != interruptedMessage || strings.Contains(strings.Join(logs, "\n"), "alias notice") {
		t.Fatalf("interrupted backup gained alias guidance or lost its error: %+v, logs %v", run, logs)
	}
}

func TestServingBackupLogsAliasesWhenRunCannotBeRecorded(t *testing.T) {
	f := newFixture(t)
	remote, err := f.manager.Path("project")
	noErr(t, err)
	git(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	schedule := f.configure(t, ScheduleChange{})
	// No running record exists, so FinishBackupRun must reject the result.
	run := state.BackupRun{ID: strings.Repeat("e", 32), Kind: state.BackupRunManual, Status: state.BackupRunning,
		Destination: f.destination, BackupName: namePrefix + "unrecorded-alias", Verification: state.BackupVerifyNotRun, StartedAt: f.clock.Now()}
	var logs []string
	f.service.Logf = func(format string, arguments ...any) { logs = append(logs, fmt.Sprintf(format, arguments...)) }
	f.service.execute(context.Background(), run, schedule)
	log := strings.Join(logs, "\n")
	if !strings.Contains(log, "ended succeeded but could not be recorded") || !strings.Contains(log, "alias notice: project: refs/heads/alias -> refs/heads/main. git symbolic-ref -- 'refs/heads/alias' 'refs/heads/main'") {
		t.Fatalf("recording failure hid the successful capture notice: %s", log)
	}
}
