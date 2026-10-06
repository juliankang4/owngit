package importsync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/state"
)

// A running scheduler keeps looking for due schedules: a schedule that
// becomes due long after Start runs at a later tick, without a wake.
func TestSchedulerRunsAScheduleThatBecomesDueAfterStart(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	// The scheduler reads the clock from its own goroutine.
	var now atomic.Int64
	now.Store(f.now.Unix())
	f.service.Clock = func() time.Time { return time.Unix(now.Load(), 0).UTC() }
	ctx := context.Background()
	_, err := f.service.SetSchedule(ctx, "project", true, time.Minute)
	noErr(t, err)
	scheduler := &Scheduler{Service: f.service, Interval: 10 * time.Millisecond}
	noErr(t, scheduler.Start(ctx))
	t.Cleanup(func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := scheduler.Stop(stopContext); err != nil {
			t.Errorf("scheduler stop: %v", err)
		}
	})
	// Start finds the schedule due at once, since it never ran.
	awaitCompletedScheduledRuns(t, f, 1)
	// The next run is due only when its interval has passed.
	now.Add(int64(2 * time.Minute / time.Second))
	awaitCompletedScheduledRuns(t, f, 2)
}

// awaitCompletedScheduledRuns waits until the project has count completed
// scheduled runs.
func awaitCompletedScheduledRuns(t *testing.T, f *fixture, count int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		runs, _, err := f.store.ImportRuns(context.Background(), "project", 10)
		noErr(t, err)
		completed := 0
		for _, run := range runs {
			if run.Kind == state.ImportKindScheduled && run.Status == state.ImportRunComplete {
				completed++
			}
		}
		if completed >= count {
			return
		}
		require(t, !time.Now().After(deadline), "%d of %d scheduled runs completed: %+v", completed, count, runs)
		time.Sleep(20 * time.Millisecond)
	}
}
