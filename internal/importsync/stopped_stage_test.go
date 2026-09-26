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
