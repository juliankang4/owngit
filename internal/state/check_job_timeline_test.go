package state

import (
	"context"
	"testing"
	"time"
)

func TestPreStartFailureRecoveryRejectsIncompleteFacts(t *testing.T) {
	for _, status := range []string{CheckJobError, CheckJobUnavailable, CheckJobInterrupted} {
		t.Run(status, func(t *testing.T) {
			fixture := newCheckJobFixture(t)
			fixture.setPolicy(t, nil)
			fixture.grantConsent(t)
			runner, _ := fixture.issueRunner(t)
			fixture.admit(t, pushJobRequest())
			ctx := context.Background()
			claimed, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now.Add(time.Second))
			if err != nil || !found {
				t.Fatalf("claim found=%v err=%v", found, err)
			}
			_, err = fixture.store.FailCheckJobBeforeStart(ctx, CheckJobCompletionAuthority{
				JobID: claimed.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation,
			}, status, "Preflight failed.", fixture.now.Add(3*time.Second))
			noErr(t, err)
			snapshot, err := fixture.store.RecoverySnapshot(ctx)
			noErr(t, err)
			for name, mutate := range map[string]func(*CheckJob){
				"missing claim":       func(j *CheckJob) { j.ClaimedAt = nil },
				"missing credential":  func(j *CheckJob) { j.CredentialID = "" },
				"missing generation":  func(j *CheckJob) { j.CredentialGeneration = 0 },
				"negative generation": func(j *CheckJob) { j.CredentialGeneration = -1 },
				"cancellation":        func(j *CheckJob) { j.CancelRequestedAt = j.FinishedAt },
				"missing lease":       func(j *CheckJob) { j.LeaseID = ""; j.LeaseExpiresAt = nil },
				"partial lease":       func(j *CheckJob) { j.LeaseExpiresAt = nil },
				"missing finish":      func(j *CheckJob) { j.FinishedAt = nil },
				"start only":          func(j *CheckJob) { j.StartedAt = j.ClaimedAt },
				"attempt only":        func(j *CheckJob) { j.AttemptID = claimed.ID },
				"protection":          func(j *CheckJob) { j.Protection = ProtectionHost },
				"lease loss":          func(j *CheckJob) { j.LeaseLostAt = j.LeaseExpiresAt },
				"interruption":        func(j *CheckJob) { j.InterruptedAt = j.FinishedAt },
				"early finish":        func(j *CheckJob) { j.FinishedAt = &j.AdmittedAt },
				"passed":              func(j *CheckJob) { j.Status = CheckJobPassed },
				"failed":              func(j *CheckJob) { j.Status = CheckJobFailed },
				"incomplete":          func(j *CheckJob) { j.Status = CheckJobIncomplete },
			} {
				t.Run(name, func(t *testing.T) {
					invalid := cloneJobRecoveryState(snapshot)
					mutate(&invalid.CheckJobs[0])
					if err := ValidateCheckRecovery(invalid); err == nil {
						t.Fatal("accepted incomplete pre-start terminal history")
					}
				})
			}
		})
	}
}

func TestCheckJobRecoveryLeaseLossOrdering(t *testing.T) {
	fixture := newJobRecoveryFixture(t)
	job := fixture.snapshot.CheckJobs[recoveryJobIndex(t, fixture.snapshot, fixture.terminalID)]
	beforeStart := job.StartedAt.Add(-time.Nanosecond)
	job.LeaseLostAt = &beforeStart
	if err := validateCheckJobTimelineAndLease(job); err == nil {
		t.Fatal("accepted loss before execution")
	}
	job.Status, job.StartedAt, job.FinishedAt, job.AttemptID, job.Protection = CheckJobAmbiguous, nil, nil, "", ProtectionUnknown
	job.LeaseLostAt = job.LeaseExpiresAt
	noErr(t, validateCheckJobTimelineAndLease(job))
	wrongDeadline := job.LeaseExpiresAt.Add(time.Nanosecond)
	job.LeaseLostAt = &wrongDeadline
	if err := validateCheckJobTimelineAndLease(job); err == nil {
		t.Fatal("accepted unstarted loss away from expiry")
	}
}
