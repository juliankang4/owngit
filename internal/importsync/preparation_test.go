package importsync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

// The scheduler passes over a repository that is still being prepared after
// startup: nothing is claimed, fetched or recorded for it and its schedule
// stays due. A healthy schedule behind a full page of preparing ones still
// runs, and the passed-over schedule runs once its repository is ready.
func TestSchedulerPassesOverAPreparingRepository(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.scheduleOtherAndProject()
	var preparing atomic.Bool
	preparing.Store(true)
	startFixturePreparation(t, f, func(_ context.Context, id, _ string) error {
		if id == "other" && preparing.Load() {
			return errors.New("synthetic preparation failure")
		}
		return nil
	}, 10*time.Second)
	require(t, f.manager.Preparing("other") && !f.manager.Preparing("project"),
		"fixture preparation did not hold only the first repository")
	// One schedule per page: the preparing repository fills the first page.
	scheduler := &Scheduler{Service: f.service, Batch: 1}
	fetches := f.transport.calls
	f.pump(scheduler, 1)
	runs, _, err := f.store.ImportRuns(ctx, "other", 5)
	require(t, err == nil && len(runs) == 0, "a preparing repository recorded runs: %+v err=%v", runs, err)
	schedule, exists, err := f.store.ImportSchedule(ctx, "other")
	require(t, err == nil && exists && schedule.LastStartedAt == nil,
		"the schedule of a preparing repository was claimed: exists=%v schedule=%+v err=%v", exists, schedule, err)
	runs = f.scheduledRuns("project")
	require(t, len(runs) == 1 && runs[0].Status == state.ImportRunComplete && f.transport.calls == fetches+1,
		"the healthy repository behind it did not run alone: runs=%+v fetches=%d", runs, f.transport.calls-fetches)

	preparing.Store(false)
	waitUntil(t, "the repository is ready", func() bool { return !f.manager.Preparing("other") })
	f.pump(scheduler, 1)
	runs = f.scheduledRuns("other")
	require(t, len(runs) == 1 && !runs[0].FinishedAt.IsZero(), "the ready repository did not run: runs=%+v", runs)
}

// startFixturePreparation starts repository preparation with step for the
// fixture and stops it when the test ends.
func startFixturePreparation(t *testing.T, f *fixture, step repository.PreparationStep, grace time.Duration) {
	t.Helper()
	f.manager.PreparationRetry = 10 * time.Millisecond
	preparation, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stop, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := f.manager.StopPreparation(stop); err != nil {
			t.Error(err)
		}
	})
	noErr(t, f.manager.StartPreparation(preparation, step, grace, nil))
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		require(t, !time.Now().After(deadline), "timed out waiting until %s", what)
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRecoveredInitialImportRetainsWaitForBufferedReadiness(t *testing.T) {
	for _, test := range []struct {
		name       string
		before     bool
		registered bool
		retry      bool
	}{
		{name: "before rename", before: true},
		{name: "after rename"},
		{name: "after repository record", registered: true},
		{name: "failed first preparation", retry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			wanted := f.commit("one", "one\n")
			crash := func() error { return errors.New("synthetic publication crash") }
			if test.before {
				f.service.beforeInitialRename = crash
			} else {
				f.service.beforeInitialRepositoryRecord = crash
			}
			_, err := f.importProject(ImportInput{})
			require(t, problemCode(err) == CodeUnresolved, "publication crash err=%v", err)
			f.service.beforeInitialRename, f.service.beforeInitialRepositoryRecord = nil, nil
			run := f.lastRun()
			if test.registered {
				noErr(t, f.store.AddRepository(ctx, state.Repository{ID: "project", Name: "project", CreatedAt: f.now}))
			}
			entered, release, ready := make(chan struct{}, 1), make(chan struct{}), make(chan struct{}, 1)
			f.manager.OnChange = func(string) { ready <- struct{}{} }
			f.manager.OnReady = f.manager.OnChange
			var attempts atomic.Int32
			startFixturePreparation(t, f, func(ctx context.Context, _, _ string) error {
				if test.retry && attempts.Add(1) == 1 {
					return errors.New("synthetic preparation failure")
				}
				entered <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}, 0)
			finished := make(chan error, 1)
			go func() { finished <- f.service.Reconcile(ctx) }()
			select {
			case err := <-finished:
				noErr(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("import recovery waited for repository preparation")
			}
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("recovered repository preparation did not start")
			}
			_, _, _, err = f.manager.ExistingPath(ctx, "project")
			require(t, errors.Is(err, repository.ErrRepositoryPreparing), "unprepared repository lookup err=%v", err)
			close(release)
			select {
			case <-ready:
			case <-time.After(10 * time.Second):
				t.Fatal("recovered repository did not become ready")
			}
			require(t, !f.service.registerPreparationWaitBeforeReadiness("project"), "ready repository stayed preparing")
			_, retainedWait := f.service.preparingReconciliation.Load("project")
			require(t, retainedWait, "buffered readiness lost the earlier reconciliation wait")
			noErr(t, f.service.ReconcileReady(ctx))
			stored, _, err := f.store.ImportRun(ctx, run.ID)
			noErr(t, err)
			require(t, stored.Status == state.ImportRunComplete, "recovered run status=%s", stored.Status)
			intent, exists, err := f.store.CompletedImportIntentForRun(ctx, run.ID)
			require(t, err == nil && exists && intent.Status == state.ImportIntentComplete,
				"recovered intent=%+v exists=%v err=%v", intent, exists, err)
			rows, err := f.store.ImportInitialDestinationsForRun(ctx, run.ID)
			require(t, err == nil && len(rows) == 1 && rows[0].State == state.ImportInitialPublished,
				"recovered destination=%+v err=%v", rows, err)
			require(t, f.destinationRefs()["refs/heads/main"] == wanted, "recovered refs changed")
			f.git(f.destinationPath(), "--git-dir", ".", "fsck", "--strict")
			f.manager.OnChange, f.manager.OnReady = nil, nil
			f.commit("two", "two\n")
			refreshed, err := f.refresh()
			require(t, err == nil && refreshed.Status == state.ImportRunComplete, "refresh after recovery=%+v err=%v", refreshed, err)
		})
	}
}
