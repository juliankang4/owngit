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

// A scheduled refresh of a repository that is still being prepared after
// startup is skipped before the claim: no run or failure is recorded and the
// schedule stays due.
func TestScheduledImportSkipsAPreparingRepository(t *testing.T) {
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
	if _, err := f.service.SetSchedule(ctx, "other", true, time.Minute); err != nil {
		t.Fatal(err)
	}
	f.manager.PreparationRetry = time.Hour
	preparation, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		if err := f.manager.StopPreparation(ctx); err != nil {
			t.Error(err)
		}
	}()
	failing := func(context.Context, string, string) error { return errors.New("synthetic preparation failure") }
	if err := f.manager.StartPreparation(preparation, failing, 10*time.Second, nil); err != nil {
		t.Fatal(err)
	}
	if !f.manager.Preparing("other") {
		t.Fatal("fixture repository is not preparing")
	}
	if _, err := f.service.StartDue(ctx, 10); err != nil {
		t.Fatal(err)
	}
	runs, _, err := f.store.ImportRuns(ctx, "other", 5)
	if err != nil || len(runs) != 0 {
		t.Fatalf("a preparing repository recorded runs: %+v err=%v", runs, err)
	}
	schedule, exists, err := f.store.ImportSchedule(ctx, "other")
	if err != nil || !exists || schedule.LastStartedAt != nil {
		t.Fatalf("the schedule of a preparing repository was claimed: exists=%v schedule=%+v err=%v", exists, schedule, err)
	}
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
	if err := f.manager.StartPreparation(preparation, step, grace, nil); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
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
	if _, err := f.importProject(ImportInput{}); problemCode(err) != CodeUnresolved {
		t.Fatalf("row crash err=%v", err)
	}
	f.service.beforeInitialRepositoryRecord = nil
	var failing atomic.Bool
	failing.Store(true)
	startFixturePreparation(t, f, func(context.Context, string, string) error {
		if failing.Load() {
			return errors.New("synthetic preparation failure")
		}
		return nil
	}, 5*time.Second)
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := f.store.Repository(context.Background(), "project"); err != nil || !exists {
		t.Fatalf("recovery did not register the repository: exists=%v err=%v", exists, err)
	}
	if _, _, _, err := f.manager.ExistingPath(context.Background(), "project"); !errors.Is(err, repository.ErrRepositoryPreparing) {
		t.Fatalf("recovered repository lookup error=%v before preparation succeeded", err)
	}
	failing.Store(false)
	waitUntil(t, "the recovered repository is served", func() bool { return !f.manager.Preparing("project") })
	if got := f.destinationRefs()["refs/heads/main"]; got != wanted {
		t.Fatalf("recovered main=%s want %s", got, wanted)
	}
}

// Startup import recovery does not wait for a repository whose preparation
// attempt hangs while holding the repository lock.
func TestImportRecoveryDoesNotWaitForAHungPreparation(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.service.beforeInitialRepositoryRecord = func() error { return errors.New("synthetic row write crash") }
	if _, err := f.importProject(ImportInput{}); problemCode(err) != CodeUnresolved {
		t.Fatalf("row crash err=%v", err)
	}
	f.service.beforeInitialRepositoryRecord = nil
	// The process stopped after the repository row was written but before
	// the destination was marked published.
	if err := f.store.AddRepository(context.Background(), state.Repository{ID: "project", Name: "project", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
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
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("import recovery waited for a hung repository preparation")
	}
	close(release)
	waitUntil(t, "the repository is served", func() bool { return !f.manager.Preparing("project") })
	// The next start completes the recovery that was left unresolved.
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.ImportInitialDestinations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.RepositoryID == "project" && row.State != state.ImportInitialPublished {
			t.Fatalf("the destination was not published after the repository became ready: %+v", row)
		}
	}
}
