package importsync

import (
	"context"
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
