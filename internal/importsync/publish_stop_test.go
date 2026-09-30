package importsync

import (
	"context"
	"database/sql"
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

// sqliteResult is an error with a SQLite result code, as the driver reports.
type sqliteResult int

func (code sqliteResult) Error() string { return fmt.Sprintf("sqlite result (%d)", int(code)) }
func (code sqliteResult) Code() int     { return int(code) }

// A state read that the run's stop cut is that stop even when the database
// answers without the context's error: SQLite reports an interrupted
// statement, and database/sql a transaction it already rolled back. This
// holds at admission and in a later stage; the read gets the run's context
// already ended, and the context is live or a write for comparison.
func TestStateReadCutByTheRunsStopIsTheStop(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired := newHookDeadline()
	close(expired.done)
	for _, answer := range []error{sqliteResult(9), sql.ErrTxDone} {
		failure := fmt.Errorf("read: %w", answer)
		for _, stop := range []struct {
			ctx  context.Context
			want string
		}{{cancelled, CodeCancelled}, {expired, CodeLimit}} {
			if got := admissionStop(stop.ctx, failure); got == nil || got.Code != stop.want {
				t.Errorf("admission read %v after %v: %v", answer, stop.ctx.Err(), got)
			}
			read := runStateReadProblem("the repository's kept history and default branch protection could not be read", failure)
			if got := problemCode(stoppedStageFailure(stop.ctx, "fetching", read)); got != stop.want {
				t.Errorf("stage read %v after %v: %s", answer, stop.ctx.Err(), got)
			}
			write := newProblem(CodeStateUnavailable, "import run stage could not be recorded", failure)
			if got := problemCode(stoppedStageFailure(stop.ctx, "fetching", write)); got != CodeStateUnavailable {
				t.Errorf("stage write %v after %v: %s", answer, stop.ctx.Err(), got)
			}
		}
		live := context.Background()
		if got := admissionStop(live, failure); got != nil {
			t.Errorf("admission read %v while live: %v", answer, got)
		}
		if got := problemCode(stoppedStageFailure(live, "fetching", runStateReadProblem("read", failure))); got != CodeStateUnavailable {
			t.Errorf("stage read %v while live: %s", answer, got)
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
