package state

import (
	"context"
	"testing"
	"time"
)

// A runner whose clock is an hour behind or ahead of the server's does not
// move the job's record: the job finishes when OwnGit received the
// completion, its attempt's duration is measured on the server's clock,
// the attempt keeps the time the runner reported, a backup of it restores,
// and the feed reads the failure exactly once.
func TestCheckJobFinishIsTheTimeOwnGitRecordedIt(t *testing.T) {
	for name, skew := range map[string]time.Duration{"behind": -time.Hour, "ahead": time.Hour} {
		t.Run(name, func(t *testing.T) {
			fixture := newCheckJobFixture(t)
			store, ctx := fixture.store, context.Background()
			fixture.setPolicy(t, nil)
			fixture.grantConsent(t)
			runner, _ := fixture.issueRunner(t)
			job := fixture.admit(t, pushJobRequest())
			_, attempt := fixture.claimAndStart(t, job, runner, "")
			received := fixture.now.Add(90 * time.Second)
			reported := received.Add(skew)

			exit := 1
			completion := CheckCompletion{
				AttemptID: attempt.ID, RepositoryID: attempt.RepositoryID, TaskID: attempt.TaskID,
				Results:    []CheckResult{{Name: "unit", Command: "go test ./...", Status: AttemptFailed, ExitCode: &exit, DurationMS: 10, OutputExcerpt: "out"}},
				FinishedAt: reported, WorktreeState: WorktreeClean, Log: "log",
			}
			stored, found, err := store.CheckJob(ctx, "project", job.ID)
			if err != nil || !found {
				t.Fatalf("job found=%v err=%v", found, err)
			}
			authority := CheckJobCompletionAuthority{JobID: stored.ID, LeaseID: stored.LeaseID, CredentialID: stored.CredentialID, CredentialGeneration: stored.CredentialGeneration}
			_, completed, err := store.CompleteCheckJobAttempt(ctx, completion, authority, received)
			noErr(t, err)
			if !completed.FinishedAt.Equal(reported) || completed.DurationMS != (90*time.Second).Milliseconds() {
				t.Fatalf("attempt finished %v after %d ms", completed.FinishedAt, completed.DurationMS)
			}
			finished, _, err := store.CheckJob(ctx, "project", job.ID)
			noErr(t, err)
			if finished.Status != CheckJobFailed || finished.FinishedAt == nil || !finished.FinishedAt.Equal(received) {
				t.Fatalf("job %s finished %v, want %v", finished.Status, finished.FinishedAt, received)
			}
			snapshot, err := store.RecoverySnapshot(ctx)
			noErr(t, err)
			noErr(t, ValidateCheckRecovery(snapshot))

			// Consecutive windows, as the feed reads them a minute apart,
			// read the failure once.
			reads := 0
			for from := fixture.now.Add(-2 * time.Hour); from.Before(fixture.now.Add(2 * time.Hour)); from = from.Add(time.Minute) {
				_, total, err := store.FeedRecords(ctx, NotifyCheckFailed, from, from.Add(time.Minute), 10)
				noErr(t, err)
				reads += total
			}
			if reads != 1 {
				t.Fatalf("the failed check was read %d times", reads)
			}
		})
	}
}
