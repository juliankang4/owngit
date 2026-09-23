package state

import (
	"context"
	"strings"
	"testing"
)

func TestParentLegacyRunOrderCannotClearAcceptedIncompleteness(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, Repository{ID: "project", Name: "project", CreatedAt: testImportNow()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfigureImportSource(ctx, ImportSourceInput{RepositoryID: "project", URL: "https://example.invalid/project.git", Mode: ImportModeStandalone, Now: testImportNow()}); err != nil {
		t.Fatal(err)
	}
	oldID, latestID := strings.Repeat("f", 32), strings.Repeat("0", 32)
	for index, id := range []string{oldID, latestID} {
		run := testImportRun(t, id, "project", 1, ImportKindRefresh, ImportRunPreparing)
		if err := store.BeginImportRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		run.Status = ImportRunComplete
		run.FinishedAt = testImportNow()
		run.LFSInspectionDone = true
		run.LFSDetected = int64(index)
		if err := store.FinishImportRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	before, exists, err := store.LatestCompletedImportRun(ctx, "project")
	if err != nil || !exists || before.ID != latestID || before.LFSDetected != 1 {
		t.Fatalf("current provenance=%+v exists=%v err=%v", before, exists, err)
	}
	snapshot, err := store.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// This is the exact run ordering used by the retained parent8 snapshot writer.
	rows, err := store.db.QueryContext(ctx, importRunSelect+` ORDER BY repository_id,started_at,id`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.ImportRuns = nil
	for rows.Next() {
		run, scanErr := scanImportRun(rows)
		if scanErr != nil {
			rows.Close()
			t.Fatal(scanErr)
		}
		snapshot.ImportRuns = append(snapshot.ImportRuns, run)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil || closeErr != nil {
		t.Fatalf("legacy rows: %v %v", rowsErr, closeErr)
	}
	if len(snapshot.ImportRuns) != 2 || snapshot.ImportRuns[0].ID != latestID || snapshot.ImportRuns[1].ID != oldID {
		t.Fatal("legacy-order fixture did not reverse the tied admissions")
	}
	// Legacy format 9 archives did not carry admission-order provenance.
	snapshot.ImportRunOrderKnown = false
	restored := openTestStore(t)
	if err := restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot); err != nil {
		t.Fatal(err)
	}
	after, exists, err := restored.LatestCompletedImportRun(ctx, "project")
	if err == nil && exists && after.LFSInspectionDone && after.LFSDetected == 0 {
		t.Fatalf("legacy ordering falsely cleared accepted incompleteness: before=%s pointers=%d after=%s pointers=%d", before.ID, before.LFSDetected, after.ID, after.LFSDetected)
	}
}
