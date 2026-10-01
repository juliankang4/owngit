package state

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCheckJobClockCorrectionRecovery(t *testing.T) {
	for _, transition := range []string{"claim", "cancel", "restart", "start", "renew", "started restart", "started cancellation", "preflight", "completion", "policy interruption"} {
		t.Run(transition, func(t *testing.T) {
			fixture := newCheckJobFixture(t)
			fixture.setPolicy(t, nil)
			fixture.grantConsent(t)
			runner, _ := fixture.issueRunner(t)
			job := fixture.admit(t, pushJobRequest())
			ctx := context.Background()
			earlier := fixture.now.Add(-time.Hour)
			var err error
			switch transition {
			case "claim":
				var found bool
				job, found, err = fixture.store.ClaimCheckJob(ctx, "project", runner.ID, earlier)
				if !found {
					t.Fatalf("claim found=%v err=%v", found, err)
				}
			case "cancel":
				job, err = fixture.store.CancelCheckJob(ctx, "project", job.ID, earlier)
			case "policy interruption":
				_, err = fixture.store.RevokeCheckConsent(ctx, "project", earlier)
			default:
				var found bool
				job, found, err = fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
				if err != nil || !found {
					t.Fatalf("claim found=%v err=%v", found, err)
				}
				authority := CheckJobCompletionAuthority{JobID: job.ID, LeaseID: job.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation}
				if transition == "restart" {
					_, err = fixture.store.ReconcileCheckJobRestart(ctx, earlier)
				} else if transition == "renew" {
					job, err = fixture.store.RenewCheckJobLease(ctx, authority, earlier)
				} else if transition == "preflight" {
					job, err = fixture.store.FailCheckJobBeforeStart(ctx, authority, CheckJobUnavailable, "Source unavailable.", earlier)
				} else {
					startedAt := fixture.now
					if transition == "start" {
						startedAt = earlier
					}
					attemptID, idErr := RandomID()
					noErr(t, idErr)
					var attempt CheckAttempt
					job, attempt, err = fixture.store.StartCheckJob(ctx, CheckJobStart{RepositoryID: "project", JobID: job.ID, LeaseID: job.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation, AttemptID: attemptID}, startedAt)
					noErr(t, err)
					switch transition {
					case "started restart":
						_, err = fixture.store.ReconcileCheckJobRestart(ctx, earlier)
					case "started cancellation":
						job, err = fixture.store.CancelCheckJob(ctx, "project", job.ID, earlier)
					case "completion":
						completeJobAttempt(t, fixture.store, attempt, AttemptFailed, earlier)
					}
				}
			}
			noErr(t, err)
			job, found, err := fixture.store.CheckJob(ctx, "project", job.ID)
			if err != nil || !found {
				t.Fatalf("read job found=%v err=%v", found, err)
			}
			snapshot, err := fixture.store.RecoverySnapshot(ctx)
			noErr(t, err)
			noErr(t, ValidateCheckRecovery(snapshot))
			if !reflect.DeepEqual(snapshot.CheckJobs[0], job) {
				t.Fatal("snapshot changed recorded job facts")
			}
			destination := openTestStore(t)
			noErr(t, destination.RestoreRecoveryState(ctx, t.TempDir(), snapshot))
			restored, found, err := destination.CheckJob(ctx, "project", job.ID)
			if err != nil || !found {
				t.Fatalf("restore found=%v err=%v", found, err)
			}
			if terminalCheckJob(job.Status) {
				if !reflect.DeepEqual(restored, job) {
					t.Fatal("restore changed terminal history")
				}
			} else if restored.Status != CheckJobInterrupted || restored.LeaseID != "" || restored.LeaseExpiresAt != nil || restored.FinishedAt == nil || !restored.FinishedAt.Equal(latestCheckJobActivity(job)) {
				t.Fatalf("restore revived execution or lost causal activity: %+v", restored)
			}
			noErr(t, ValidateCheckRecovery(mustClockSnapshot(t, destination)))
		})
	}
}

func mustClockSnapshot(t *testing.T, store *Store) RecoveryState {
	t.Helper()
	snapshot, err := store.RecoverySnapshot(context.Background())
	noErr(t, err)
	return snapshot
}

func TestCheckJobClockCorrectionDoesNotReviveExpiredLease(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(map[bool]string{false: "claimed", true: "started"}[started], func(t *testing.T) {
			fixture := newCheckJobFixture(t)
			fixture.setPolicy(t, nil)
			fixture.grantConsent(t)
			runner, _ := fixture.issueRunner(t)
			fixture.admit(t, pushJobRequest())
			ctx := context.Background()
			job, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
			if err != nil || !found {
				t.Fatalf("claim found=%v err=%v", found, err)
			}
			id, err := RandomID()
			noErr(t, err)
			request := CheckJobStart{RepositoryID: "project", JobID: job.ID, LeaseID: job.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation, AttemptID: id}
			if started {
				job, _, err = fixture.store.StartCheckJob(ctx, request, fixture.now)
				noErr(t, err)
			}
			authority := CheckJobCompletionAuthority{JobID: job.ID, LeaseID: job.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation}
			deadline := *job.LeaseExpiresAt
			_, err = fixture.store.RenewCheckJobLease(ctx, authority, deadline)
			if !errors.Is(err, ErrCheckJobLease) {
				t.Fatalf("renewed expired lease: %v", err)
			}
			_, err = fixture.store.RenewCheckJobLease(ctx, authority, fixture.now.Add(-time.Hour))
			if !errors.Is(err, ErrCheckJobState) {
				t.Fatalf("renewed lost authority after correction: %v", err)
			}
			if _, _, err := fixture.store.StartCheckJob(ctx, request, fixture.now.Add(-time.Hour)); err == nil {
				t.Fatal("clock correction granted execution again")
			}
			lost, _, err := fixture.store.CheckJob(ctx, "project", job.ID)
			noErr(t, err)
			if lost.Status != CheckJobAmbiguous || !lost.LeaseExpiresAt.Equal(deadline) || lost.LeaseLostAt == nil || !lost.LeaseLostAt.Equal(deadline) {
				t.Fatalf("expired authority changed: %+v", lost)
			}
			noErr(t, ValidateCheckRecovery(mustClockSnapshot(t, fixture.store)))
		})
	}
}
