package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// One run runs at a time, a restart interrupts it, and a long message is
// cut whole characters at a time.
func TestBackupRunRecords(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	noErr(t, err)
	defer store.Close()
	start := time.Unix(1800000000, 0)
	first := BackupRun{ID: strings.Repeat("1", 32), Kind: BackupRunManual, Destination: "/backups", BackupName: "owngit-backup-1", StartedAt: start}
	noErr(t, store.StartBackupRun(ctx, first))
	second := first
	second.ID, second.Kind = strings.Repeat("2", 32), BackupRunScheduled
	if err := store.StartBackupRun(ctx, second); !errors.Is(err, ErrBackupRunning) {
		t.Fatalf("second running backup: %v", err)
	}
	first.Status, first.Verification, first.FinishedAt = BackupFailed, BackupVerifyFailed, start.Add(time.Minute)
	first.Message = strings.Repeat("검", 200)
	first.HoldKnown, first.LongestHold, first.LongestHoldRepository = true, 1500*time.Millisecond, "project"
	noErr(t, store.FinishBackupRun(ctx, first))
	if err := store.FinishBackupRun(ctx, first); err == nil {
		t.Fatal("a finished run was finished again")
	}
	noErr(t, store.StartBackupRun(ctx, second))
	if count, err := store.InterruptBackupRuns(ctx, "stopped", start.Add(time.Hour)); err != nil || count != 1 {
		t.Fatalf("interrupted %d: %v", count, err)
	}
	runs, err := store.BackupRuns(ctx)
	noErr(t, err)
	if len(runs) != 2 || runs[0].ID != second.ID || runs[0].Status != BackupInterrupted || runs[0].HoldKnown {
		t.Fatalf("runs: %+v", runs)
	}
	stored := runs[1]
	if len(stored.Message) > MaxBackupRunMessage || !utf8.ValidString(stored.Message) || !stored.HoldKnown ||
		stored.LongestHold != 1500*time.Millisecond || stored.LongestHoldRepository != "project" {
		t.Fatalf("finished run: %d bytes, %+v", len(stored.Message), stored)
	}
}

// Runs are listed in the order they started, also when they started in the
// same second, whatever their IDs.
func TestBackupRunsInTheOrderTheyStarted(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	noErr(t, err)
	defer store.Close()
	start := time.Unix(1800000000, 0)
	for _, id := range []string{strings.Repeat("f", 32), strings.Repeat("0", 32)} {
		run := BackupRun{ID: id, Kind: BackupRunManual, Destination: "/backups", BackupName: "owngit-backup-" + id[:8], StartedAt: start}
		noErr(t, store.StartBackupRun(ctx, run))
		run.Status, run.Verification, run.FinishedAt = BackupSucceeded, BackupVerifyNotRun, start
		noErr(t, store.FinishBackupRun(ctx, run))
	}
	noErr(t, store.ForgetBackup(ctx, strings.Repeat("f", 32)))
	runs, err := store.BackupRuns(ctx)
	noErr(t, err)
	if len(runs) != 2 || runs[0].ID != strings.Repeat("0", 32) || runs[1].BackupName != "" || runs[0].BackupName == "" {
		t.Fatalf("runs: %+v", runs)
	}
}

// Every recorded outcome keeps the history bounded: the newest records
// stay, whether their runs succeeded or failed, and the oldest go, except
// the newest scheduled run, whose start decides when the next scheduled
// backup is due, and the record of a backup OwnGit still keeps.
func TestRecordedBackupRunsKeepTheNewestRecords(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	noErr(t, err)
	defer store.Close()
	start := time.Unix(1800000000, 0)
	at := func(index int, kind string) BackupRun {
		return BackupRun{ID: fmt.Sprintf("%032x", index), Kind: kind, Destination: "/backups", StartedAt: start.Add(time.Duration(index) * time.Second)}
	}
	finish := func(run BackupRun) BackupRun {
		noErr(t, store.StartBackupRun(ctx, run))
		run.Status, run.Verification, run.FinishedAt = BackupFailed, BackupVerifyNotRun, run.StartedAt
		noErr(t, store.FinishBackupRun(ctx, run))
		return run
	}
	// The oldest records: a scheduled run whose start decides when the next
	// scheduled backup is due, and a run whose backup is still in its
	// folder.
	scheduled := finish(at(1, BackupRunScheduled))
	kept := at(2, BackupRunManual)
	kept.BackupName, kept.ManifestSHA256 = "owngit-backup-kept", strings.Repeat("a", 64)
	kept = finish(kept)
	for index := 3; index <= 107; index++ {
		finish(at(index, BackupRunManual))
	}
	runs, err := store.BackupRuns(ctx)
	noErr(t, err)
	failures := 0
	present := map[string]bool{}
	for _, run := range runs {
		present[run.ID] = true
		if run.Kind == BackupRunManual && run.Status == BackupFailed && run.BackupName == "" {
			failures++
		}
	}
	if failures != 100 {
		t.Fatalf("%d failures of %d records kept", failures, len(runs))
	}
	for _, id := range []string{fmt.Sprintf("%032x", 107), kept.ID, scheduled.ID} {
		if !present[id] {
			t.Fatalf("record %s was removed: %+v", id, runs)
		}
	}
	for index := 3; index <= 7; index++ {
		if present[fmt.Sprintf("%032x", index)] {
			t.Fatalf("the old failure %d was kept: %+v", index, runs)
		}
	}
	// The latest 100 records, the latest scheduled backup and the backup
	// OwnGit still keeps, as the run history documents them.
	if len(runs) != 102 {
		t.Fatalf("%d records kept", len(runs))
	}
	// A start records the run a previous process left as interrupted, and
	// the limit applies to that record too: the interrupted run stays, and
	// the oldest failure goes.
	running := at(108, BackupRunManual)
	noErr(t, store.StartBackupRun(ctx, running))
	if count, err := store.InterruptBackupRuns(ctx, "OwnGit stopped before this backup finished.", start.Add(200*time.Second)); err != nil || count != 1 {
		t.Fatalf("a start recorded %d runs as interrupted: %v", count, err)
	}
	runs, err = store.BackupRuns(ctx)
	noErr(t, err)
	if len(runs) != 102 {
		t.Fatalf("%d records kept after a start", len(runs))
	}
	if interrupted := runs[0]; interrupted.ID != running.ID || interrupted.Status != BackupInterrupted || interrupted.Message == "" {
		t.Fatalf("the interrupted run: %+v", interrupted)
	}
	for _, run := range runs {
		if run.ID == fmt.Sprintf("%032x", 8) {
			t.Fatalf("the old failure 8 was kept after a start: %+v", runs)
		}
	}
}
