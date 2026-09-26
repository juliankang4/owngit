package importsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"owngit/internal/state"
)

// A refresh stopped while publication reads the state database is recorded
// as the stop, not as a state database failure, and the destination is
// unchanged. The stop is injected at the read, so the test does not depend on
// timing.
func TestRefreshStoppedAtAPublicationStateReadIsCancelled(t *testing.T) {
	f := newFixture(t)
	f.commit("source", "initial\n")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
	before := f.git(f.destinationPath(), "--git-dir", ".", "rev-parse", "refs/heads/main")
	f.commit("source", "next\n")
	f.service.beforeObservationRead = func(ctx context.Context) {
		f.service.beforeObservationRead = nil
		if _, err := f.service.Cancel(context.Background(), "project"); err != nil {
			t.Errorf("cancel: %v", err)
		}
		<-ctx.Done()
	}
	run, err := f.refresh()
	if problemCode(err) != CodeCancelled || run.Status != state.ImportRunCancelled || run.ErrorClass != CodeCancelled {
		t.Fatalf("refresh stopped at a state read run=%s/%s err=%v", run.Status, run.ErrorClass, err)
	}
	if got := f.git(f.destinationPath(), "--git-dir", ".", "rev-parse", "refs/heads/main"); got != before {
		t.Fatalf("destination changed: %s want %s", got, before)
	}
	if status, err := f.service.Status(context.Background(), "project"); err != nil || status.UnresolvedIntents != 0 {
		t.Fatalf("stopped refresh left unresolved=%d err=%v", status.UnresolvedIntents, err)
	}
}

// Only a state read that the run's own stop cut is reclassified. A genuine
// state database failure stays state_unavailable, even when the run was
// stopped, and so does a bookkeeping write whose error is joined with the
// stop's error, or a read cut by another context's deadline.
func TestStoppedStageFailureKeepsGenuineStateFailures(t *testing.T) {
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()
	disk := newProblem(CodeStateUnavailable, "recorded observations could not be read", errors.New("disk I/O error"))
	interrupted := runStateReadProblem("recorded observations could not be read", fmt.Errorf("query: %w", context.Canceled))
	stop := newProblem(CodeCancelled, "import stopped", context.Canceled)
	bookkeeping := newProblem(CodeStateUnavailable, "partial publication state could not be recorded", errors.Join(stop, errors.New("sql: database is closed")))
	otherDeadline := runStateReadProblem("recorded observations could not be read", context.DeadlineExceeded)
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{"stop interrupted the read", stopped, interrupted, CodeCancelled},
		{"genuine failure after a stop", stopped, disk, CodeStateUnavailable},
		{"context error while the run is live", live, interrupted, CodeStateUnavailable},
		{"unresolved outcome stays", stopped, newProblem(CodeUnresolved, "partial", interrupted), CodeUnresolved},
		{"bookkeeping write joined with the stop", stopped, bookkeeping, CodeStateUnavailable},
		{"read cut by another context's deadline", stopped, otherDeadline, CodeStateUnavailable},
	} {
		if got := problemCode(stoppedStageFailure(tc.ctx, "publishing", tc.err)); got != tc.want {
			t.Errorf("%s: code=%s want %s", tc.name, got, tc.want)
		}
	}
}

// A run stopped during publication whose bookkeeping write then genuinely
// fails reports the state failure, not a cancellation, for a refresh and for
// an initial import. Closing the store stands in for a database fault.
func TestStopWithGenuineBookkeepingFailureStaysStateUnavailable(t *testing.T) {
	for _, kind := range []string{"refresh", "initial"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			completeFixtureSetup(t, f)
			f.commit("one", "one\n")
			if kind == "refresh" {
				f.mustImport(ImportInput{})
				f.commit("two", "two\n")
			}
			// A HEAD change makes the publication stop between refs and HEAD.
			f.git(f.source, "branch", "trunk")
			f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/trunk")
			stopAndBreak := func() {
				if _, err := f.service.Cancel(context.Background(), "project"); err != nil {
					t.Errorf("cancel: %v", err)
				}
				if err := f.store.Close(); err != nil {
					t.Errorf("close store: %v", err)
				}
			}
			if kind == "refresh" {
				// A refresh's visible refs make it finish its HEAD write
				// despite the stop, so the store fails at the HEAD record.
				f.service.beforeRecord = func(_ context.Context, record string) {
					if record == "applied HEAD" {
						f.service.beforeRecord = nil
						stopAndBreak()
					}
				}
			} else {
				f.service.beforeFinalHEADLock = func() {
					f.service.beforeFinalHEADLock = nil
					stopAndBreak()
				}
			}
			var err error
			if kind == "refresh" {
				_, err = f.refresh()
			} else {
				_, err = f.importProject(ImportInput{})
			}
			if problemCode(err) != CodeStateUnavailable || !strings.Contains(err.Error(), "could not be recorded") || !strings.Contains(err.Error(), "database is closed") {
				t.Fatalf("stop with a failed bookkeeping write code=%s err=%v", problemCode(err), err)
			}
		})
	}
}
