package importsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

func TestReconcilePagesPendingIntentsAndDefersLockLogs(t *testing.T) {
	f := newFixture(t)
	f.commit("base", "base")
	imported := f.mustImport(ImportInput{})
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	mainOID := strings.TrimSpace(f.git(f.destinationPath(), "--git-dir", ".", "rev-parse", "refs/heads/main"))
	head := (headIdentity{kind: headSymbolic, target: "refs/heads/main", oid: mainOID}).encode()
	for index, id := range []string{strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)} {
		if err := f.store.CreateImportIntent(ctx, state.ImportIntent{
			ID: id, RepositoryID: "project", RunID: imported.Run.ID,
			SourceGeneration: imported.Run.SourceGeneration, AuthorityRevision: imported.Run.AuthorityRevision, Status: state.ImportIntentPlanning,
			Expected: map[string]string{"refs/heads/main": mainOID, state.ImportHeadRef: head},
			Desired:  map[string]string{"refs/heads/main": strings.Repeat("d", 40), state.ImportHeadRef: head},
			Observed: map[string]string{"refs/heads/main": mainOID, state.ImportHeadRef: head},
			Retained: map[string]string{}, CreatedAt: now.Add(time.Duration(index) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	previous := reconcilePageLimit
	reconcilePageLimit = 2
	t.Cleanup(func() { reconcilePageLimit = previous })
	var pages [][]string
	f.service.afterReconcileIntentPage = func(ids []string) { pages = append(pages, append([]string(nil), ids...)) }
	var loggedUnderLock bool
	f.service.Logf = func(string, ...any) {
		if !f.manager.Locks.For("project").TryLock() {
			loggedUnderLock = true
			return
		}
		f.manager.Locks.For("project").Unlock()
	}
	f.service.Clock = func() time.Time {
		if !f.manager.Locks.For("project").TryLock() {
			t.Error("clock ran while the repository lock was held")
		} else {
			f.manager.Locks.For("project").Unlock()
		}
		return now
	}
	if _, err := f.service.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.service.stagingRootPath(), "odd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if loggedUnderLock {
		t.Fatal("reconciliation logged while a repository lock was held")
	}
	if len(pages) != 2 || len(pages[0]) != 2 || len(pages[1]) != 1 {
		t.Fatalf("intent pages=%v", pages)
	}
	again := len(pages)
	if err := f.service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pages) != again {
		t.Fatal("second reconcile re-read resolved intents")
	}
}

func TestSetScheduleRefusesRepositoryWithoutSource(t *testing.T) {
	f := newFixture(t)
	if _, err := f.manager.Create(context.Background(), "project", "Project"); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.SetSchedule(context.Background(), "project", true, time.Minute)
	if problemCode(err) != CodeNotConfigured {
		t.Fatalf("schedule without source error=%v", err)
	}
}

func TestScheduledClaimAdvancesBeforePreparation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.mustImport(ImportInput{})
	if _, err := f.manager.Create(ctx, "other", "Other"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ConfigureSource(ctx, ConfigureInput{
		RepositoryID: "other", URL: "https://example.invalid/team/other.git",
		Mode: ModeStandalone, GitOnlyConsent: true, AllowPrivateNetwork: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"other", "project"} {
		if _, err := f.service.SetSchedule(ctx, id, true, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	f.service.beforeScheduledPreparation = func(repositoryID string) error {
		if repositoryID == "other" {
			schedule, exists, err := f.store.ImportSchedule(ctx, repositoryID)
			if err != nil || !exists || schedule.LastStartedAt == nil {
				t.Errorf("preparation ran before the schedule was claimed: exists=%v schedule=%+v err=%v", exists, schedule, err)
			}
			return errScheduledPreparation
		}
		return nil
	}
	if _, err := f.service.StartDue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.StartDue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	runs, _, err := f.store.ImportRuns(ctx, "project", 5)
	if err != nil || len(runs) == 0 || runs[0].Kind != state.ImportKindScheduled {
		t.Fatalf("later repository was not claimed after the earlier failure: runs=%+v err=%v", runs, err)
	}
	other, _, err := f.store.ImportRuns(ctx, "other", 5)
	if err != nil || len(other) != 1 || other[0].Status != state.ImportRunFailed {
		t.Fatalf("failed claim was not recorded: runs=%+v err=%v", other, err)
	}
}

var errScheduledPreparation = context.DeadlineExceeded

func TestUnclaimedDueQueryStaysOnTheSameRepository(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.manager.Create(ctx, "other", "Other"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ConfigureSource(ctx, ConfigureInput{
		RepositoryID: "other", URL: "https://example.invalid/team/other.git",
		Mode: ModeStandalone, GitOnlyConsent: true, AllowPrivateNetwork: true,
	}); err != nil {
		t.Fatal(err)
	}
	f.mustImport(ImportInput{})
	for _, id := range []string{"other", "project"} {
		if _, err := f.service.SetSchedule(ctx, id, true, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	now := f.service.clock()
	first, err := f.store.DueImportSchedules(ctx, now, 1)
	second, secondErr := f.store.DueImportSchedules(ctx, now, 1)
	if err != nil || secondErr != nil || len(first) != 1 || len(second) != 1 || first[0].RepositoryID != "other" || second[0].RepositoryID != "other" {
		t.Fatalf("unclaimed due query did not stay on the same repository: first=%+v second=%+v err=%v/%v", first, second, err, secondErr)
	}
}

func TestStatusAndHistoryUseBoundedPages(t *testing.T) {
	f := newFixture(t)
	f.mustImport(ImportInput{})
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	observations := make([]state.ImportObservation, 0, statusRefLimit+1)
	for index := 0; index < statusRefLimit+1; index++ {
		observations = append(observations, state.ImportObservation{
			RepositoryID: "project", SourceGeneration: 1, RefName: "refs/heads/b" + padObservation(index),
			OID: strings.Repeat("a", 40), ObservedAt: now,
		})
	}
	if err := f.store.RecordImportObservations(ctx, observations); err != nil {
		t.Fatal(err)
	}
	status, err := f.service.Status(ctx, "project")
	if err != nil || !status.RefsTruncated || len(status.Refs) > statusRefLimit {
		t.Fatalf("status did not bound observations: truncated=%v refs=%d err=%v", status.RefsTruncated, len(status.Refs), err)
	}
	for index := 0; index < 3; index++ {
		run := state.ImportRun{
			ID: strings.Repeat(string(rune('a'+index)), 32), RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1,
			Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing, StartedAt: now.Add(time.Duration(index) * time.Second),
			CreatedAt: now,
		}
		if err := f.store.BeginImportRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		run.Status = state.ImportRunFailed
		run.FinishedAt = now
		run.LFSInspectionDone = true
		run.ErrorClass = CodeRuntimeUnavailable
		run.Message = "failed"
		if err := f.store.FinishImportRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	page, more, err := f.service.History(ctx, "project", 1)
	if err != nil || len(page) != 1 || !more || page[0].RowID == 0 {
		t.Fatalf("history page=%+v more=%v err=%v", page, more, err)
	}
	next, _, err := f.service.HistoryBefore(ctx, "project", 10000, page[0].RowID)
	if err != nil || len(next) == 0 || next[0].RowID >= page[0].RowID || len(next) > maxHistoryPage {
		t.Fatalf("history cursor did not move older: next=%+v err=%v", next, err)
	}
}

func padObservation(index int) string {
	return strings.Repeat("0", 3-len(itoa(index))) + itoa(index)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
