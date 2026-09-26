package importsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// A run stopped while local Git verification runs is recorded as the stop
// that happened, not as content that failed verification.
func TestStopDuringVerificationIsNotAContentFailure(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		f := newFixture(t)
		f.commit("one", "one\n")
		f.service.beforeStagingVerification = func(context.Context) {
			if cancelled, err := f.service.Cancel(context.Background(), "project"); err != nil || !cancelled {
				t.Errorf("cancel during verification cancelled=%v err=%v", cancelled, err)
			}
		}
		_, err := f.importProject(ImportInput{})
		run := f.lastRun()
		if problemCode(err) != CodeCancelled || run.Status != state.ImportRunCancelled || run.ErrorClass != CodeCancelled {
			t.Fatalf("cancel during verification err=%v status=%s class=%s message=%q", err, run.Status, run.ErrorClass, run.Message)
		}
		if strings.Count(run.Message, "import cancelled") > 2 {
			t.Fatalf("stored message repeats its cause: %q", run.Message)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		f := newFixture(t)
		f.commit("one", "one\n")
		f.service.beforeStagingVerification = func(ctx context.Context) {
			select {
			case <-ctx.Done():
			case <-time.After(30 * time.Second):
				t.Error("run deadline did not expire")
			}
		}
		_, err := f.importProject(ImportInput{Limits: Limits{RunTimeout: 3 * time.Second}})
		run := f.lastRun()
		if problemCode(err) != CodeLimit || run.Status != state.ImportRunFailed || run.ErrorClass != CodeLimit || !strings.Contains(run.Message, "deadline") {
			t.Fatalf("deadline during verification err=%v status=%s class=%s message=%q", err, run.Status, run.ErrorClass, run.Message)
		}
	})
}

// A run whose deadline or cancellation comes while a stage is being recorded
// reports that stop and records its outcome, not a state failure.
func TestStopWhileRecordingAStageIsNotAStateFailure(t *testing.T) {
	for _, stage := range []string{state.ImportRunFetching, state.ImportRunInspecting} {
		t.Run("deadline at "+stage, func(t *testing.T) {
			f := newFixture(t)
			f.commit("one", "one\n")
			f.service.beforeRecord = func(ctx context.Context, status string) {
				if status != stage {
					return
				}
				select {
				case <-ctx.Done():
				case <-time.After(30 * time.Second):
					t.Error("run deadline did not expire")
				}
			}
			_, err := f.importProject(ImportInput{Limits: Limits{RunTimeout: 3 * time.Second}})
			run := f.lastRun()
			if problemCode(err) != CodeLimit || run.Status != state.ImportRunFailed || run.ErrorClass != CodeLimit || !strings.Contains(run.Message, "deadline") {
				t.Fatalf("deadline while recording %s: err=%v status=%s class=%s message=%q", stage, err, run.Status, run.ErrorClass, run.Message)
			}
		})
		t.Run("cancel at "+stage, func(t *testing.T) {
			f := newFixture(t)
			f.commit("one", "one\n")
			f.service.beforeRecord = func(_ context.Context, status string) {
				if status != stage {
					return
				}
				if cancelled, err := f.service.Cancel(context.Background(), "project"); err != nil || !cancelled {
					t.Errorf("cancel while recording %s cancelled=%v err=%v", stage, cancelled, err)
				}
			}
			_, err := f.importProject(ImportInput{})
			run := f.lastRun()
			if problemCode(err) != CodeCancelled || run.Status != state.ImportRunCancelled || run.ErrorClass != CodeCancelled {
				t.Fatalf("cancel while recording %s: err=%v status=%s class=%s message=%q", stage, err, run.Status, run.ErrorClass, run.Message)
			}
		})
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
			if problemCode(err) != CodeStateUnavailable || run.Status != state.ImportRunFailed || run.ErrorClass != CodeStateUnavailable || !strings.Contains(run.Message, "synthetic stage failure") {
				t.Fatalf("failed stage write: err=%v status=%s class=%s message=%q", err, run.Status, run.ErrorClass, run.Message)
			}
		})
	}
}

// A run whose deadline or cancellation comes while its start is being
// recorded reports that stop and records its outcome, not a state failure.
func TestStopWhileRecordingTheStartIsNotAStateFailure(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		f := newFixture(t)
		f.commit("one", "one\n")
		f.service.beforeRunRecord = func(ctx context.Context) {
			select {
			case <-ctx.Done():
			case <-time.After(30 * time.Second):
				t.Error("run deadline did not expire")
			}
		}
		_, err := f.importProject(ImportInput{Limits: Limits{RunTimeout: 3 * time.Second}})
		if problemCode(err) != CodeLimit {
			t.Fatalf("deadline while recording the start: err=%v", err)
		}
		run := f.lastRun()
		if run.Status != state.ImportRunFailed || run.ErrorClass != CodeLimit || !strings.Contains(run.Message, "deadline") {
			t.Fatalf("deadline while recording the start: err=%v status=%s class=%s message=%q", err, run.Status, run.ErrorClass, run.Message)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		f := newFixture(t)
		f.commit("one", "one\n")
		f.service.beforeRunRecord = func(context.Context) { cancelAdmittedRun(t, f) }
		_, err := f.importProject(ImportInput{})
		if problemCode(err) != CodeCancelled {
			t.Fatalf("cancel while recording the start: err=%v", err)
		}
		run := f.lastRun()
		if run.Status != state.ImportRunCancelled || run.ErrorClass != CodeCancelled {
			t.Fatalf("cancel while recording the start: err=%v status=%s class=%s message=%q", err, run.Status, run.ErrorClass, run.Message)
		}
	})
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
			if problemCode(err) != CodeStateUnavailable || !strings.Contains(err.Error(), "synthetic start failure") {
				t.Fatalf("failed start record: err=%v", err)
			}
			if count, err := f.store.TableRowCount(context.Background(), "import_runs"); err != nil || count != 0 {
				t.Fatalf("run rows after a failed start record=%d err=%v", count, err)
			}
		})
	}
}

// cancelAdmittedRun cancels the one admitted run the way Cancel does. Cancel
// itself cannot run here: admission holds the lifecycle lock it takes.
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
// Deadlines from 1 ns to 30 ms end the run at different admission steps.
func TestDeadlineDuringAdmissionIsTheTimeLimit(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
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
		if run := f.lastRun(); rows != rowsBefore+1 || run.Status != state.ImportRunFailed || run.ErrorClass != CodeLimit {
			t.Fatalf("%s with a %s deadline: %d new runs, last status=%s class=%s", what, timeout, rows-rowsBefore, run.Status, run.ErrorClass)
		}
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
	f.mustImport(ImportInput{})
	f.commit("two", "two\n")
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
				hit := false
				f.service.beforeRecord = func(ctx context.Context, record string) {
					if record != test.record || hit {
						return
					}
					hit = true
					if stop == "cancel" {
						cancelAdmittedRun(t, f)
						return
					}
					select {
					case <-ctx.Done():
					case <-time.After(30 * time.Second):
						t.Error("run deadline did not expire")
					}
				}
				limits := Limits{}
				if stop == "deadline" {
					limits.RunTimeout = 3 * time.Second
				}
				var err error
				if test.refresh {
					_, err = f.service.Refresh(context.Background(), "project", limits)
				} else {
					_, err = f.importProject(ImportInput{Limits: limits})
				}
				if !hit {
					t.Fatalf("the run did not record the %s", test.record)
				}
				wantCode, wantStatus := CodeLimit, state.ImportRunFailed
				if stop == "cancel" {
					wantCode, wantStatus = CodeCancelled, state.ImportRunCancelled
				}
				run := f.lastRun()
				if problemCode(err) != wantCode || run.Status != wantStatus || run.ErrorClass != wantCode {
					t.Fatalf("%s: err=%v status=%s class=%s message=%q", name, err, run.Status, run.ErrorClass, run.Message)
				}
				intents, err := f.store.PendingImportIntents(context.Background(), "project")
				if err != nil || len(intents) != 0 {
					t.Fatalf("%s: pending intents=%+v err=%v", name, intents, err)
				}
				if !test.refresh {
					assertNoLeftoverDirectories(t, f)
					rows, err := f.store.ImportInitialDestinationsForRun(context.Background(), run.ID)
					noErr(t, err)
					for _, row := range rows {
						if row.State == state.ImportInitialPreparing || row.State == state.ImportInitialReady {
							t.Fatalf("%s: initial destination %s left %s", name, row.Name, row.State)
						}
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
				if problemCode(err) != CodeStateUnavailable || run.Status != state.ImportRunFailed || run.ErrorClass != CodeStateUnavailable || !strings.Contains(run.Message, "synthetic record failure") {
					t.Fatalf("%s: err=%v status=%s class=%s message=%q", name, err, run.Status, run.ErrorClass, run.Message)
				}
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
