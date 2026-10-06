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

// An initial import that stopped after its rename is registered by recovery
// after startup. The repository is served only once preparation for this
// process succeeds.
func TestRecoveredInitialRepositoryIsServedOnlyAfterPreparation(t *testing.T) {
	f := newFixture(t)
	wanted := f.commit("one", "one\n")
	f.service.beforeInitialRepositoryRecord = func() error { return errors.New("synthetic row write crash") }
	_, err := f.importProject(ImportInput{})
	require(t, problemCode(err) == CodeUnresolved, "row crash err=%v", err)
	f.service.beforeInitialRepositoryRecord = nil
	var failing atomic.Bool
	failing.Store(true)
	startFixturePreparation(t, f, func(context.Context, string, string) error {
		if failing.Load() {
			return errors.New("synthetic preparation failure")
		}
		return nil
	}, 5*time.Second)
	noErr(t, f.service.Reconcile(context.Background()))
	_, exists, err := f.store.Repository(context.Background(), "project")
	require(t, err == nil && exists, "recovery did not register the repository: exists=%v err=%v", exists, err)
	_, _, _, err = f.manager.ExistingPath(context.Background(), "project")
	require(t, errors.Is(err, repository.ErrRepositoryPreparing),
		"recovered repository lookup error=%v before preparation succeeded", err)
	failing.Store(false)
	waitUntil(t, "the recovered repository is served", func() bool { return !f.manager.Preparing("project") })
	got := f.destinationRefs()["refs/heads/main"]
	require(t, got == wanted, "recovered main=%s want %s", got, wanted)
}

// Startup import recovery does not wait for a repository whose preparation
// attempt hangs while holding the repository lock.
func TestImportRecoveryDoesNotWaitForAHungPreparation(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.service.beforeInitialRepositoryRecord = func() error { return errors.New("synthetic row write crash") }
	_, err := f.importProject(ImportInput{})
	require(t, problemCode(err) == CodeUnresolved, "row crash err=%v", err)
	f.service.beforeInitialRepositoryRecord = nil
	// The process stopped after the repository row was written but before
	// the destination was marked published.
	noErr(t, f.store.AddRepository(context.Background(),
		state.Repository{ID: "project", Name: "project", CreatedAt: f.now}))
	release := make(chan struct{})
	var entered atomic.Bool
	startFixturePreparation(t, f, func(ctx context.Context, _, _ string) error {
		entered.Store(true)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, 50*time.Millisecond)
	waitUntil(t, "the preparation attempt holds the lock", entered.Load)
	finished := make(chan error, 1)
	go func() { finished <- f.service.Reconcile(context.Background()) }()
	select {
	case err := <-finished:
		noErr(t, err)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("import recovery waited for a hung repository preparation")
	}
	close(release)
	waitUntil(t, "the repository is served", func() bool { return !f.manager.Preparing("project") })
	// The next start completes the recovery that was left unresolved.
	noErr(t, f.service.Reconcile(context.Background()))
	rows, err := f.store.ImportInitialDestinations(context.Background())
	noErr(t, err)
	for _, row := range rows {
		require(t, row.RepositoryID != "project" || row.State == state.ImportInitialPublished,
			"the destination was not published after the repository became ready: %+v", row)
	}
}
