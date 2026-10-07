package importsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestStorageUnavailableLockProblemsKeepTheirCause(t *testing.T) {
	cause := fmt.Errorf("directory disappeared: %w", repository.ErrStorageUnavailable)
	for _, problem := range []*Problem{
		preWriteLockProblem(context.Background(), cause),
		stoppedProblem(context.Background(), "before publication", cause),
	} {
		if problem.Code != CodeRepositoryMissing || problem.Message != "repository storage is unavailable" || !errors.Is(problem, repository.ErrStorageUnavailable) {
			t.Fatalf("storage lock problem=%+v", problem)
		}
	}
}

// hookDeadline is the parent context of a run whose deadline the test lets
// pass at the seam under test. A run timeout starts at admission, and a slow
// runner can spend it before the run reaches the seam. The
// run context ends with context.DeadlineExceeded from this parent exactly as
// from its own timeout; TestDeadlineDuringAdmissionIsTheTimeLimit covers the
// timeout itself.
type hookDeadline struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newHookDeadline() *hookDeadline {
	return &hookDeadline{Context: context.Background(), done: make(chan struct{})}
}

func (d *hookDeadline) Done() <-chan struct{} { return d.done }

func (d *hookDeadline) Err() error {
	select {
	case <-d.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// stopRun stops the run at a seam, as stop names: "deadline" lets the run's
// deadline pass, "cancel" cancels it. It returns once runCtx, the run's
// context, has ended, so the run continues from the seam already stopped.
func stopRun(t *testing.T, f *fixture, stop string, deadline *hookDeadline, runCtx context.Context) {
	t.Helper()
	if stop == "cancel" {
		cancelAdmittedRun(t, f)
	} else {
		deadline.once.Do(func() { close(deadline.done) })
	}
	select {
	case <-runCtx.Done():
	case <-time.After(time.Minute):
		t.Errorf("the run context did not end after the %s", stop)
	}
}

// runUnder runs a first import, or a refresh when refresh is set, with parent
// as the parent context of the run.
func runUnder(f *fixture, parent context.Context, refresh bool) error {
	if refresh {
		_, err := f.service.Refresh(parent, "project", Limits{})
		return err
	}
	_, err := f.importProjectUnder(parent, ImportInput{})
	return err
}

// assertStoppedRun fails unless the last run reported and recorded stop: a
// deadline as the time limit, a cancellation as cancelled. It returns the run.
func assertStoppedRun(t *testing.T, f *fixture, name, stop string, err error) state.ImportRun {
	t.Helper()
	run := f.lastRun()
	wantCode, wantStatus, wantMessage := CodeLimit, state.ImportRunFailed, "deadline"
	if stop == "cancel" {
		wantCode, wantStatus, wantMessage = CodeCancelled, state.ImportRunCancelled, "cancel"
	}
	require(t, problemCode(err) == wantCode && run.Status == wantStatus && run.ErrorClass == wantCode &&
		strings.Contains(run.Message, wantMessage),
		"%s: err=%v status=%s class=%s message=%q", name, err, run.Status, run.ErrorClass, run.Message)
	require(t, strings.Count(run.Message, "import cancelled") <= 2,
		"%s: stored message repeats its cause: %q", name, run.Message)
	return run
}

// A deadline or cancellation that comes while a run records its start or a
// stage, or just before it verifies its staged refs, is reported and recorded
// as that stop: never as a state failure, and never as content that failed
// verification.
func TestStopAtARunStepIsThatStop(t *testing.T) {
	for _, step := range []struct {
		name string
		at   func(f *fixture, stop func(runCtx context.Context))
	}{
		{"the start record", func(f *fixture, stop func(context.Context)) { f.service.beforeRunRecord = stop }},
		{"the fetching stage", func(f *fixture, stop func(context.Context)) {
			f.service.beforeRecord = func(ctx context.Context, record string) {
				if record == state.ImportRunFetching {
					stop(ctx)
				}
			}
		}},
		{"the inspecting stage", func(f *fixture, stop func(context.Context)) {
			f.service.beforeRecord = func(ctx context.Context, record string) {
				if record == state.ImportRunInspecting {
					stop(ctx)
				}
			}
		}},
		{"verification", func(f *fixture, stop func(context.Context)) { f.service.beforeStagingVerification = stop }},
	} {
		for _, stop := range []string{"deadline", "cancel"} {
			name := stop + " at " + step.name
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				f.commit("one", "one\n")
				deadline := newHookDeadline()
				step.at(f, func(runCtx context.Context) { stopRun(t, f, stop, deadline, runCtx) })
				assertStoppedRun(t, f, name, stop, runUnder(f, deadline, false))
			})
		}
	}
}

// A stage write that really fails stays a state failure, also when the run
// was stopped while the stage was being recorded.
func TestAFailedStageWriteIsAStateFailure(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprintf("stopped=%v", stopped), func(t *testing.T) {
			f := newFixture(t)
			f.commit("one", "one\n")
			noErr(t, f.store.Exec(context.Background(), `CREATE TRIGGER fail_stage BEFORE UPDATE ON import_runs
				WHEN NEW.status='fetching' BEGIN SELECT RAISE(FAIL,'synthetic stage failure'); END`))
			if stopped {
				f.service.beforeRecord = func(_ context.Context, status string) {
					if status == state.ImportRunFetching {
						if cancelled, err := f.service.Cancel(context.Background(), "project"); err != nil || !cancelled {
							t.Errorf("cancel while recording fetching cancelled=%v err=%v", cancelled, err)
						}
					}
				}
			}
			_, err := f.importProject(ImportInput{})
			run := f.lastRun()
			require(t, problemCode(err) == CodeStateUnavailable && run.Status == state.ImportRunFailed &&
				run.ErrorClass == CodeStateUnavailable && strings.Contains(run.Message, "synthetic stage failure"),
				"failed stage write: err=%v status=%s class=%s message=%q", err, run.Status, run.ErrorClass, run.Message)
		})
	}
}

// A start record that really fails stays a state failure, also when the run
// was stopped while it was being recorded.
func TestAFailedStartRecordIsAStateFailure(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprintf("stopped=%v", stopped), func(t *testing.T) {
			f := newFixture(t)
			f.commit("one", "one\n")
			noErr(t, f.store.Exec(context.Background(), `CREATE TRIGGER fail_start BEFORE INSERT ON import_runs
				BEGIN SELECT RAISE(FAIL,'synthetic start failure'); END`))
			if stopped {
				f.service.beforeRunRecord = func(context.Context) { cancelAdmittedRun(t, f) }
			}
			_, err := f.importProject(ImportInput{})
			require(t, problemCode(err) == CodeStateUnavailable &&
				strings.Contains(err.Error(), "synthetic start failure"), "failed start record: err=%v", err)
			count, err := f.store.TableRowCount(context.Background(), "import_runs")
			require(t, err == nil && count == 0, "run rows after a failed start record=%d err=%v", count, err)
		})
	}
}

// cancelAdmittedRun cancels the one admitted run the way Cancel does. Cancel
// itself cannot run while admission records the start: admission holds the
// lifecycle lock that Cancel takes.
func cancelAdmittedRun(t *testing.T, f *fixture) {
	t.Helper()
	cancelled := 0
	f.service.active.Range(func(_, value any) bool {
		value.(activeExecution).cancel(ErrCancelled)
		cancelled++
		return true
	})
	if cancelled != 1 {
		t.Errorf("cancelled %d admitted runs, want 1", cancelled)
	}
}

// A run deadline that passes during admission, before the run is recorded,
// is reported as the time limit, never as a state or unclassified failure.
// Deadlines from 1 ns to 30 ms end the run at different admission steps. The
// source transfer never ends, so a run that gets past admission ends by its
// deadline too instead of finishing first.
func TestDeadlineDuringAdmissionIsTheTimeLimit(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.transport.gate = make(chan struct{})
	// A run stopped after its start was recorded must be recorded as the
	// time limit; one stopped before leaves no run.
	check := func(what string, timeout time.Duration, rowsBefore int, err error) {
		t.Helper()
		var problem *Problem
		if !errors.As(err, &problem) || problem.Code != CodeLimit {
			t.Errorf("%s with a %s deadline: err=%v", what, timeout, err)
			return
		}
		rows, countErr := f.store.TableRowCount(context.Background(), "import_runs")
		noErr(t, countErr)
		if rows == rowsBefore {
			return
		}
		run := f.lastRun()
		require(t, rows == rowsBefore+1 && run.Status == state.ImportRunFailed && run.ErrorClass == CodeLimit,
			"%s with a %s deadline: %d new runs, last status=%s class=%s", what, timeout, rows-rowsBefore, run.Status, run.ErrorClass)
	}
	rows := func() int {
		count, err := f.store.TableRowCount(context.Background(), "import_runs")
		noErr(t, err)
		return count
	}
	for _, timeout := range []time.Duration{time.Nanosecond, time.Millisecond, 3 * time.Millisecond, 10 * time.Millisecond, 30 * time.Millisecond} {
		for round := 0; round < 3; round++ {
			before := rows()
			_, err := f.importProject(ImportInput{Limits: Limits{RunTimeout: timeout}})
			check("first import", timeout, before, err)
		}
	}
	f.transport.gate = nil
	f.mustImport(ImportInput{})
	f.commit("two", "two\n")
	f.transport.gate = make(chan struct{})
	for _, timeout := range []time.Duration{time.Nanosecond, time.Millisecond, 3 * time.Millisecond, 10 * time.Millisecond, 30 * time.Millisecond} {
		for round := 0; round < 3; round++ {
			before := rows()
			_, err := f.service.Refresh(context.Background(), "project", Limits{RunTimeout: timeout})
			check("refresh", timeout, before, err)
		}
	}
}

// A run whose deadline or cancellation comes while its initial destination or
// its publication intent is being recorded reports that stop, records its
// outcome and leaves nothing behind: no unpublished directory and no pending
// intent.
func TestStopWhileRecordingPublicationIsNotAStateFailure(t *testing.T) {
	for _, test := range []struct {
		record  string
		refresh bool
	}{
		{"initial destination", false},
		{"publication intent", false},
		{"publication intent", true},
	} {
		for _, stop := range []string{"deadline", "cancel"} {
			name := stop + " at " + test.record
			if test.refresh {
				name += " of a refresh"
			}
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				f.commit("one", "one\n")
				if test.refresh {
					f.mustImport(ImportInput{})
					f.commit("two", "two\n")
				}
				deadline := newHookDeadline()
				hit := false
				f.service.beforeRecord = func(ctx context.Context, record string) {
					if record == test.record && !hit {
						hit = true
						stopRun(t, f, stop, deadline, ctx)
					}
				}
				err := runUnder(f, deadline, test.refresh)
				require(t, hit, "the run did not record the %s", test.record)
				run := assertStoppedRun(t, f, name, stop, err)
				assertIntentsSettled(t, f, name)
				if !test.refresh {
					assertNoLeftoverDirectories(t, f)
					rows, err := f.store.ImportInitialDestinationsForRun(context.Background(), run.ID)
					noErr(t, err)
					for _, row := range rows {
						require(t, row.State != state.ImportInitialPreparing && row.State != state.ImportInitialReady,
							"%s: initial destination %s left %s", name, row.Name, row.State)
					}
				}
			})
		}
	}
}

// An initial destination or publication intent record that really fails
// stays a state failure, also when the run was stopped while it was being
// recorded, and nothing is left behind once the run or the next
// reconciliation has cleaned up.
func TestAFailedPublicationRecordIsAStateFailure(t *testing.T) {
	for _, test := range []struct{ record, table string }{
		{"initial destination", "import_initial_destinations"},
		{"publication intent", "import_publication_intents"},
	} {
		for _, stopped := range []bool{false, true} {
			name := fmt.Sprintf("%s stopped=%v", test.record, stopped)
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				f.commit("one", "one\n")
				noErr(t, f.store.Exec(context.Background(), `CREATE TRIGGER fail_record BEFORE INSERT ON `+test.table+`
					BEGIN SELECT RAISE(FAIL,'synthetic record failure'); END`))
				if stopped {
					f.service.beforeRecord = func(_ context.Context, record string) {
						if record == test.record {
							cancelAdmittedRun(t, f)
						}
					}
				}
				_, err := f.importProject(ImportInput{})
				run := f.lastRun()
				require(t, problemCode(err) == CodeStateUnavailable && run.Status == state.ImportRunFailed &&
					run.ErrorClass == CodeStateUnavailable && strings.Contains(run.Message, "synthetic record failure"),
					"%s: err=%v status=%s class=%s message=%q", name, err, run.Status, run.ErrorClass, run.Message)
				// A stopped first import removes its unpublished directory
				// itself; after a state failure the next reconciliation does.
				if !stopped {
					noErr(t, f.service.Reconcile(context.Background()), "reconcile after the failed record")
				}
				assertNoLeftoverDirectories(t, f)
			})
		}
	}
}

// A deadline or cancellation that comes while the run records what its ref
// transaction or HEAD write changed never blocks the finalization. A refresh,
// whose refs are visible by then, completes: the stop comes too late. A first
// import, whose repository is not visible until its directory is renamed into
// place, reports the stop and leaves nothing behind.
func TestStopWhileRecordingAnAppliedPublication(t *testing.T) {
	for _, test := range []struct {
		record  string
		refresh bool
	}{
		{"applied publication", true},
		{"applied HEAD", true},
		{"applied publication", false},
		{"applied HEAD", false},
	} {
		for _, stop := range []string{"deadline", "cancel"} {
			kind := "first import"
			if test.refresh {
				kind = "refresh"
			}
			name := stop + " at " + test.record + " of a " + kind
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				f.commit("one", "one\n")
				if test.refresh {
					f.mustImport(ImportInput{})
					f.commit("two", "two\n")
				}
				if test.record == "applied HEAD" {
					// A new source HEAD makes the run write HEAD after its refs.
					f.git(f.source, "branch", "trunk")
					f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/trunk")
				}
				deadline := newHookDeadline()
				hit := false
				f.service.beforeRecord = func(ctx context.Context, record string) {
					if record == test.record && !hit {
						hit = true
						stopRun(t, f, stop, deadline, ctx)
					}
				}
				err := runUnder(f, deadline, test.refresh)
				require(t, hit, "%s: the run did not record the %s", name, test.record)
				if test.refresh {
					run := f.lastRun()
					require(t, err == nil && run.Status == state.ImportRunComplete,
						"%s: err=%v status=%s class=%s message=%q", name, err, run.Status, run.ErrorClass, run.Message)
				} else {
					assertStoppedRun(t, f, name, stop, err)
					assertNoLeftoverDirectories(t, f)
				}
				assertIntentsSettled(t, f, name)
			})
		}
	}
}

// assertIntentsSettled fails when a publication intent of the project is still
// planning, applied or unresolved. A first import that published nothing
// settles its intent as invalidated.
func assertIntentsSettled(t *testing.T, f *fixture, name string) {
	t.Helper()
	intents, err := f.store.PendingImportIntents(context.Background(), "project")
	noErr(t, err)
	for _, intent := range intents {
		switch intent.Status {
		case state.ImportIntentInvalidated, state.ImportIntentNotApplied, state.ImportIntentComplete:
		default:
			t.Fatalf("%s: intent %s left %s (%s)", name, intent.ID, intent.Status, intent.Reason)
		}
	}
}

// A refresh that also moves HEAD and is stopped after its ref transaction,
// before its HEAD write, completes and writes HEAD: its refs are visible, so
// the stop comes too late for the HEAD write that finishes the publication.
// The stop comes while the applied refs are recorded or just before the HEAD
// lock.
func TestStopBetweenARefreshsRefsAndItsHEADCompletesIt(t *testing.T) {
	for _, at := range []string{"applied publication", "final HEAD lock"} {
		for _, stop := range []string{"deadline", "cancel"} {
			name := stop + " at " + at
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				f.commit("one", "one\n")
				f.mustImport(ImportInput{})
				f.commit("two", "two\n")
				f.git(f.source, "branch", "trunk")
				f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/trunk")
				deadline := newHookDeadline()
				// The HEAD lock seam has no context; the run's context is
				// taken from the record just before it.
				var runCtx context.Context
				hit := false
				f.service.beforeRecord = func(ctx context.Context, record string) {
					if record != "applied publication" {
						return
					}
					runCtx = ctx
					if at == record && !hit {
						hit = true
						stopRun(t, f, stop, deadline, ctx)
					}
				}
				f.service.beforeFinalHEADLock = func() {
					if at == "final HEAD lock" && !hit && runCtx != nil {
						hit = true
						stopRun(t, f, stop, deadline, runCtx)
					}
				}
				err := runUnder(f, deadline, true)
				require(t, hit, "%s: the run did not reach the stop point", name)
				run := f.lastRun()
				require(t, err == nil && run.Status == state.ImportRunComplete,
					"%s: err=%v status=%s class=%s message=%q", name, err, run.Status, run.ErrorClass, run.Message)
				eq(t, "destination HEAD", f.git(f.destinationPath(), "symbolic-ref", "HEAD"), "refs/heads/trunk")
				assertIntentsSettled(t, f, name)
				// Nothing is left for the owner to resolve: the next refresh runs.
				f.service.beforeRecord, f.service.beforeFinalHEADLock = nil, nil
				f.commit("three", "three\n")
				_, err = f.service.Refresh(context.Background(), "project", Limits{})
				require(t, err == nil, "%s: the next refresh: %v", name, err)
			})
		}
	}
}

// A changed import authority still stops the HEAD write of a refresh after
// its ref transaction: only a cancellation or deadline comes too late.
func TestAuthorityChangeBeforeARefreshsHEADWriteStopsIt(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	f.commit("two", "two\n")
	f.git(f.source, "branch", "trunk")
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/trunk")
	f.service.beforeFinalHEADLock = func() {
		f.service.beforeFinalHEADLock = nil
		noErr(t, f.store.Exec(context.Background(),
			`UPDATE import_sources SET authority_revision=authority_revision+1 WHERE repository_id='project'`))
	}
	_, err := f.service.Refresh(context.Background(), "project", Limits{})
	require(t, errors.Is(err, ErrSuperseded), "authority change before the HEAD write: err=%v", err)
	eq(t, "destination HEAD", f.git(f.destinationPath(), "symbolic-ref", "HEAD"), "refs/heads/main")
}
