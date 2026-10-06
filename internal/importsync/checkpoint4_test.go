package importsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	_, err := f.service.Prepare(ctx)
	noErr(t, err)
	noErr(t, os.Mkdir(filepath.Join(f.service.stagingRootPath(), "odd"), 0o700))
	noErr(t, f.service.Reconcile(ctx))
	require(t, !loggedUnderLock, "reconciliation logged while a repository lock was held")
	require(t, len(pages) == 2 && len(pages[0]) == 2 && len(pages[1]) == 1, "intent pages=%v", pages)
	again := len(pages)
	noErr(t, f.service.Reconcile(ctx))
	require(t, len(pages) == again, "second reconcile re-read resolved intents")
}

func TestSetScheduleRefusesRepositoryWithoutSource(t *testing.T) {
	f := newFixture(t)
	_, err := f.manager.Create(context.Background(), "project", "Project")
	noErr(t, err)
	_, err = f.service.SetSchedule(context.Background(), "project", true, time.Minute)
	require(t, problemCode(err) == CodeNotConfigured, "schedule without source error=%v", err)
}

// scheduleOtherAndProject imports project and schedules it and an unimported
// other. Both are due, and other comes first.
func (f *fixture) scheduleOtherAndProject() {
	f.t.Helper()
	ctx := context.Background()
	f.mustImport(ImportInput{})
	if _, err := f.manager.Create(ctx, "other", "Other"); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.service.ConfigureSource(ctx, ConfigureInput{
		RepositoryID: "other", URL: "https://example.invalid/team/other.git",
		Mode: ModeStandalone, GitOnlyConsent: true, AllowPrivateNetwork: true,
	}); err != nil {
		f.t.Fatal(err)
	}
	for _, id := range []string{"other", "project"} {
		if _, err := f.service.SetSchedule(ctx, id, true, time.Minute); err != nil {
			f.t.Fatal(err)
		}
	}
}

// pump runs one scheduler pass with the given concurrency and waits for the
// runs it started.
func (f *fixture) pump(scheduler *Scheduler, concurrency int) {
	scheduler.pump(context.Background(), make(chan struct{}, concurrency))
	scheduler.wait.Wait()
}

// scheduledRuns returns the scheduled runs of repositoryID, newest first.
func (f *fixture) scheduledRuns(repositoryID string) []state.ImportRun {
	f.t.Helper()
	runs, _, err := f.store.ImportRuns(context.Background(), repositoryID, 10)
	noErr(f.t, err)
	return slices.DeleteFunc(runs, func(run state.ImportRun) bool { return run.Kind != state.ImportKindScheduled })
}

// A scheduled run that fails has still claimed its schedule, so the next pass
// reaches the repository behind it.
func TestFailedScheduledRunLetsTheNextRepositoryRun(t *testing.T) {
	f := newFixture(t)
	f.scheduleOtherAndProject()
	scheduler := &Scheduler{Service: f.service, Batch: 1}
	f.transport.fail = errors.New("synthetic transfer failure")
	f.pump(scheduler, 1)
	f.transport.fail = nil
	f.pump(scheduler, 1)
	other := f.scheduledRuns("other")
	require(t, len(other) == 1 && other[0].Status == state.ImportRunFailed,
		"the failed run was not recorded: runs=%+v", other)
	project := f.scheduledRuns("project")
	require(t, len(project) == 1 && project[0].Status == state.ImportRunComplete,
		"the later repository did not run after the earlier failure: runs=%+v", project)
}

// A claimed schedule whose refresh is refused before it records a run, here
// because the service is shutting down, is recorded as a failed run, so the
// claimed interval does not pass without a record.
func TestRefusedScheduledRefreshIsRecordedAsFailed(t *testing.T) {
	f := newFixture(t)
	f.scheduleOtherAndProject()
	noErr(t, f.service.Shutdown(context.Background()))
	f.pump(&Scheduler{Service: f.service, Batch: 1}, 1)
	schedule, exists, err := f.store.ImportSchedule(context.Background(), "other")
	require(t, err == nil && exists && schedule.LastStartedAt != nil,
		"the schedule was not claimed: exists=%v schedule=%+v err=%v", exists, schedule, err)
	other := f.scheduledRuns("other")
	require(t, len(other) == 1 && other[0].Status == state.ImportRunFailed &&
		strings.Contains(other[0].Message, "shutting down"),
		"the refused refresh was not recorded as failed: runs=%+v", other)
}

// With every run slot taken, a pass leaves the due schedules untouched and
// records no run. Once a slot frees, the waiting schedules run.
func TestSchedulerLeavesDueSchedulesUntouchedWhileSlotsAreFull(t *testing.T) {
	f := newFixture(t)
	f.scheduleOtherAndProject()
	ctx := context.Background()
	scheduler := &Scheduler{Service: f.service, Batch: 4}
	full := make(chan struct{}, 1)
	full <- struct{}{}
	scheduler.pump(ctx, full)
	for _, id := range []string{"other", "project"} {
		schedule, exists, err := f.store.ImportSchedule(ctx, id)
		require(t, err == nil && exists && schedule.LastStartedAt == nil,
			"%s schedule was claimed with no free slot: exists=%v schedule=%+v err=%v", id, exists, schedule, err)
		runs := f.scheduledRuns(id)
		require(t, len(runs) == 0, "%s recorded a run that never started: %+v", id, runs)
	}
	require(t, len(full) == 1, "a full pass changed the slots it did not own")
	f.pump(scheduler, 1)
	other := f.scheduledRuns("other")
	require(t, len(other) == 1 && other[0].Status == state.ImportRunComplete,
		"the first due schedule did not run first: runs=%+v", other)
	project := f.scheduledRuns("project")
	require(t, len(project) == 0, "the second schedule ran in the single slot's first pass: runs=%+v", project)
	f.pump(scheduler, 1)
	project = f.scheduledRuns("project")
	require(t, len(project) == 1 && project[0].Status == state.ImportRunComplete,
		"the waiting schedule starved: runs=%+v", project)
}

// Stop returns only after a scheduled run in flight finished.
func TestSchedulerStopJoinsItsRun(t *testing.T) {
	f := newFixture(t)
	f.scheduleOtherAndProject()
	f.transport.gate = make(chan struct{})
	fetching := make(chan struct{})
	f.transport.before = func() { close(fetching) }
	scheduler := &Scheduler{Service: f.service, Interval: time.Hour, Batch: 2, Concurrency: 1}
	noErr(t, scheduler.Start(context.Background()))
	select {
	case <-fetching:
	case <-time.After(10 * time.Second):
		t.Fatal("the first scheduled run never fetched")
	}
	stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	noErr(t, scheduler.Stop(stop))
	other := f.scheduledRuns("other")
	require(t, len(other) == 1 && !other[0].FinishedAt.IsZero(),
		"Stop returned before the run in flight finished: runs=%+v", other)
}

func TestUnclaimedDueQueryStaysOnTheSameRepository(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.manager.Create(ctx, "other", "Other")
	noErr(t, err)
	if _, err := f.service.ConfigureSource(ctx, ConfigureInput{
		RepositoryID: "other", URL: "https://example.invalid/team/other.git",
		Mode: ModeStandalone, GitOnlyConsent: true, AllowPrivateNetwork: true,
	}); err != nil {
		t.Fatal(err)
	}
	f.mustImport(ImportInput{})
	for _, id := range []string{"other", "project"} {
		_, err := f.service.SetSchedule(ctx, id, true, time.Minute)
		noErr(t, err)
	}
	now := f.service.clock()
	first, err := f.store.DueImportSchedulesAfter(ctx, now, nil, 1)
	second, secondErr := f.store.DueImportSchedulesAfter(ctx, now, nil, 1)
	require(t, err == nil && secondErr == nil && len(first) == 1 && len(second) == 1 &&
		first[0].RepositoryID == "other" && second[0].RepositoryID == "other",
		"unclaimed due query did not stay on the same repository: first=%+v second=%+v err=%v/%v", first, second, err, secondErr)
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
	noErr(t, f.store.RecordImportObservations(ctx, observations))
	status, err := f.service.Status(ctx, "project")
	require(t, err == nil && status.RefsTruncated && len(status.Refs) <= statusRefLimit,
		"status did not bound observations: truncated=%v refs=%d err=%v", status.RefsTruncated, len(status.Refs), err)
	for index := 0; index < 3; index++ {
		run := state.ImportRun{
			ID: strings.Repeat(string(rune('a'+index)), 32), RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1,
			Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing, StartedAt: now.Add(time.Duration(index) * time.Second),
			CreatedAt: now,
		}
		noErr(t, f.store.BeginImportRun(ctx, run))
		run.Status = state.ImportRunFailed
		run.FinishedAt = now
		run.LFSInspectionDone = true
		run.ErrorClass = CodeRuntimeUnavailable
		run.Message = "failed"
		noErr(t, f.store.FinishImportRun(ctx, run))
	}
	page, more, err := f.service.History(ctx, "project", 1)
	require(t, err == nil && len(page) == 1 && more && page[0].RowID != 0,
		"history page=%+v more=%v err=%v", page, more, err)
	next, _, err := f.service.HistoryBefore(ctx, "project", 10000, page[0].RowID)
	require(t, err == nil && len(next) != 0 && next[0].RowID < page[0].RowID && len(next) <= maxHistoryPage,
		"history cursor did not move older: next=%+v err=%v", next, err)
}

// History above one page must return every run exactly once, newest first,
// when a caller follows the cursor with a limit larger than maxHistoryPage.
func TestHistoryPagingReturnsEveryRunBeyondOnePage(t *testing.T) {
	f := newFixture(t)
	f.mustImport(ImportInput{})
	ctx := context.Background()
	existing, more, err := f.service.History(ctx, "project", maxHistoryPage)
	require(t, err == nil && !more, "initial history=%+v more=%v err=%v", existing, more, err)
	const added = 2*maxHistoryPage + 37
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	want := make([]string, 0, added+len(existing))
	for index := 0; index < added; index++ {
		run := state.ImportRun{
			ID: fmt.Sprintf("%032x", index+1), RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1,
			Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing, StartedAt: now.Add(time.Duration(index) * time.Second),
		}
		noErr(t, f.store.BeginImportRun(ctx, run))
		run.Status = state.ImportRunFailed
		run.FinishedAt = run.StartedAt
		run.LFSInspectionDone = true
		run.ErrorClass = CodeRuntimeUnavailable
		run.Message = "failed"
		noErr(t, f.store.FinishImportRun(ctx, run))
		want = append([]string{run.ID}, want...)
	}
	for _, run := range existing {
		want = append(want, run.ID)
	}

	var got []string
	var pages []int
	var cursor int64
	for {
		page, more, err := f.service.HistoryBefore(ctx, "project", 10000, cursor)
		noErr(t, err)
		require(t, len(page) != 0 && len(page) <= maxHistoryPage, "page %d has %d runs", len(pages), len(page))
		pages = append(pages, len(page))
		for _, run := range page {
			require(t, cursor == 0 || run.RowID < cursor,
				"run %s rowid %d is not older than cursor %d", run.ID, run.RowID, cursor)
			cursor = run.RowID
			got = append(got, run.ID)
		}
		if !more {
			break
		}
	}
	wantPages := []int{maxHistoryPage, maxHistoryPage, len(want) - 2*maxHistoryPage}
	require(t, slices.Equal(pages, wantPages), "page sizes=%v, want %v", pages, wantPages)
	require(t, slices.Equal(got, want),
		"paged history has %d runs, want %d in newest-first order without duplicates or gaps", len(got), len(want))
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
