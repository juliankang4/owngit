package state_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/backups"
	"owngit/internal/gitexec"
	"owngit/internal/recovery"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestCheckJobClockCorrectionBackupRestore(t *testing.T) {
	for _, transition := range []string{"claim", "cancel", "restart", "start", "renew"} {
		t.Run(transition, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			root := t.TempDir()
			store, manager := clockBackupStore(t, ctx, root)
			admitted := time.Unix(1_800_000_000, 0)
			corrected := admitted.Add(-time.Hour)
			clockBackupRequire(t, store.SaveBackupSchedule(ctx, state.BackupSchedule{
				Destination: filepath.Join(root, "backups"), Interval: 24 * time.Hour, Keep: 7, Verify: true,
			}))
			service := &backups.Service{Store: store, Repositories: manager, Now: func() time.Time { return corrected }, Logf: t.Logf}
			clockBackupRequire(t, service.Start(ctx))
			t.Cleanup(func() { clockBackupRequire(t, service.Stop(context.Background())) })
			// A clean control uses the same real backup service and Git repositories.
			clockBackupRun(t, ctx, service)
			job := clockBackupTransition(t, ctx, store, transition, admitted, corrected)
			run := clockBackupRun(t, ctx, service)
			backup := filepath.Join(run.Destination, run.BackupName)
			verified, err := recovery.Verify(ctx, backup, filepath.Join(root, "verify"), "")
			clockBackupRequire(t, err)
			if !verified.Verified {
				t.Fatalf("backup verification failed: %+v", verified)
			}
			stateDir := filepath.Join(root, "restored-state")
			_, err = recovery.RestoreWithReport(ctx, backup, stateDir, filepath.Join(root, "restored-repositories"), "")
			clockBackupRequire(t, err)
			restoredStore, err := state.Open(ctx, stateDir)
			clockBackupRequire(t, err)
			defer restoredStore.Close()
			restored, found, err := restoredStore.CheckJob(ctx, "project", job.ID)
			if err != nil || !found {
				t.Fatalf("restored job found=%v err=%v", found, err)
			}
			if job.Status == state.CheckJobCancelled || job.Status == state.CheckJobInterrupted {
				if !reflect.DeepEqual(restored, job) {
					t.Fatal("terminal history changed during backup/restore")
				}
			} else if restored.Status != state.CheckJobInterrupted || restored.LeaseID != "" || restored.LeaseExpiresAt != nil {
				t.Fatalf("unfinished work regained execution authority: %+v", restored)
			}
			if !restored.AdmittedAt.Equal(job.AdmittedAt) || !reflect.DeepEqual(restored.ClaimedAt, job.ClaimedAt) || !reflect.DeepEqual(restored.StartedAt, job.StartedAt) {
				t.Fatal("restore adjusted observed timestamps")
			}
			if job.AttemptID != "" {
				attempt, found, err := restoredStore.CheckAttemptByID(ctx, "project", job.AttemptID)
				if err != nil || !found || attempt.Status != state.AttemptPending || len(attempt.Results) != 0 {
					t.Fatalf("restore invented check results: found=%v attempt=%+v err=%v", found, attempt, err)
				}
			}
			_, err = restoredStore.RecoverySnapshot(ctx)
			clockBackupRequire(t, err)
		})
	}
}

func TestCheckJobNormalClockBackupFormats(t *testing.T) {
	for _, version := range []int{10, 11} {
		t.Run(map[int]string{10: "format10", 11: "format11"}[version], func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store, manager := clockBackupStore(t, ctx, root)
			now := time.Unix(1_800_000_000, 0)
			job := clockBackupTransition(t, ctx, store, "cancel", now, now.Add(time.Minute))
			if version == 11 {
				protect := true
				_, err := store.SaveRepositoryRefPolicy(ctx, "project", state.RepositoryRefPolicyChange{ProtectDefaultBranch: &protect})
				clockBackupRequire(t, err)
			}
			backup := filepath.Join(root, "normal-backup")
			_, err := recovery.CreateWithReport(ctx, store, manager, backup)
			clockBackupRequire(t, err)
			content, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
			clockBackupRequire(t, err)
			var manifest recovery.Manifest
			clockBackupRequire(t, json.Unmarshal(content, &manifest))
			if manifest.Version != version || len(manifest.CheckJobs) != 1 {
				t.Fatalf("format=%d jobs=%d, want format=%d with one job", manifest.Version, len(manifest.CheckJobs), version)
			}
			stateDir := filepath.Join(root, "normal-restored-state")
			_, err = recovery.RestoreWithReport(ctx, backup, stateDir, filepath.Join(root, "normal-restored-repositories"), "")
			clockBackupRequire(t, err)
			restored, err := state.Open(ctx, stateDir)
			clockBackupRequire(t, err)
			defer restored.Close()
			got, found, err := restored.CheckJob(ctx, "project", job.ID)
			if err != nil || !found || !reflect.DeepEqual(got, job) {
				t.Fatalf("normal history changed: found=%v job=%+v err=%v", found, got, err)
			}
		})
	}
}

func clockBackupStore(t *testing.T, ctx context.Context, root string) (*state.Store, *repository.Manager) {
	t.Helper()
	storage := filepath.Join(root, "repositories")
	clockBackupRequire(t, os.MkdirAll(storage, 0o700))
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	clockBackupRequire(t, err)
	t.Cleanup(func() { clockBackupRequire(t, store.Close()) })
	adminHash, err := auth.HashPassword("synthetic-admin-password")
	clockBackupRequire(t, err)
	clockBackupRequire(t, store.CompleteSetup(ctx, storage, "open", "", adminHash, false))
	clockBackupRequire(t, os.Mkdir(filepath.Join(root, "backups"), 0o700))
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	clockBackupRequire(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: storage}
	for _, id := range []string{"project", "empty"} {
		_, err := manager.Create(ctx, id, "")
		clockBackupRequire(t, err)
	}
	return store, manager
}

func clockBackupTransition(t *testing.T, ctx context.Context, store *state.Store, transition string, admitted, corrected time.Time) state.CheckJob {
	t.Helper()
	_, err := store.SaveCheckPolicyAndGrantConsent(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorHost, AllowedEvents: []string{"push"}, RunWorkflows: new(false),
		MaxTimeoutMS: 600000, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
	}, nil, admitted)
	clockBackupRequire(t, err)
	job, deduped, err := store.AdmitCheckJob(ctx, state.CheckJobRequest{
		RepositoryID: "project", Trigger: "push", EventKey: "clock-observation", SourceOID: strings.Repeat("a", 40),
		TriggerRef: "main", WorkflowDigest: strings.Repeat("b", 64), Checks: []state.CheckDefinition{{Name: "noop", Command: "true"}},
	}, admitted)
	if err != nil || deduped {
		t.Fatalf("admission deduped=%v err=%v", deduped, err)
	}
	if transition == "cancel" {
		_, err = store.CancelCheckJob(ctx, "project", job.ID, corrected)
	} else {
		when := admitted
		if transition == "claim" {
			when = corrected
		}
		claimed, found, claimErr := store.ClaimLocalCheckJob(ctx, "project", when)
		if claimErr != nil || !found {
			t.Fatalf("claim found=%v err=%v", found, claimErr)
		}
		switch transition {
		case "restart":
			_, err = store.ReconcileCheckJobRestart(ctx, corrected)
		case "start":
			id, idErr := state.RandomID()
			clockBackupRequire(t, idErr)
			_, _, err = store.StartCheckJob(ctx, state.CheckJobStart{
				RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: claimed.CredentialID,
				CredentialGeneration: claimed.CredentialGeneration, AttemptID: id,
			}, corrected)
		case "renew":
			_, err = store.RenewCheckJobLease(ctx, state.CheckJobCompletionAuthority{
				JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: claimed.CredentialID, CredentialGeneration: claimed.CredentialGeneration,
			}, corrected)
		}
	}
	clockBackupRequire(t, err)
	job, found, err := store.CheckJob(ctx, "project", job.ID)
	if err != nil || !found {
		t.Fatalf("job found=%v err=%v", found, err)
	}
	return job
}

func clockBackupRun(t *testing.T, ctx context.Context, service *backups.Service) backups.RunView {
	t.Helper()
	run, err := service.StartNow()
	clockBackupRequire(t, err)
	for {
		// The stored result can finish before retention releases the service slot.
		runs, err := service.Runs(ctx)
		clockBackupRequire(t, err)
		var stored backups.RunView
		for _, candidate := range runs {
			if candidate.ID == run.ID {
				stored = candidate
				break
			}
		}
		if stored.ID == "" {
			t.Fatal("backup record missing")
		}
		if stored.Status != state.BackupRunning {
			if stored.Status != state.BackupSucceeded || stored.Verification != state.BackupVerifyPassed {
				t.Fatalf("backup status=%s verification=%s message=%s", stored.Status, stored.Verification, stored.Message)
			}
			return stored
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func clockBackupRequire(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
