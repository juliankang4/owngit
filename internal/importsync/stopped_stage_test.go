package importsync

import (
	"context"
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
			f.service.beforeStageRecord = func(ctx context.Context, status string) {
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
			f.service.beforeStageRecord = func(_ context.Context, status string) {
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
				f.service.beforeStageRecord = func(_ context.Context, status string) {
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
