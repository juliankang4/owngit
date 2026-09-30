package state

import (
	"context"
	"errors"
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
