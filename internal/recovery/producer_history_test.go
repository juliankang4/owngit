package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestCheckJobProducerHistoryRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, preStartStatus string
		restartOffset        time.Duration
		legacy, complete     bool
	}{
		{name: "prestart_unavailable", preStartStatus: state.CheckJobUnavailable},
		{name: "prestart_error", preStartStatus: state.CheckJobError},
		{name: "prestart_interrupted", preStartStatus: state.CheckJobInterrupted},
		{name: "restart_before_expiry", restartOffset: 10 * time.Second},
		{name: "restart_after_expiry", restartOffset: 90 * time.Second},
		// Earlier versions committed the start and the attempt in two
		// transactions, so a database or backup can hold a started job
		// without an attempt. It must still recover and round trip.
		{name: "legacy_started_without_attempt_restart", restartOffset: 10 * time.Second, legacy: true},
		{name: "legacy_started_without_attempt_expiry", legacy: true},
		{name: "late_completion_before_expiry", restartOffset: 10 * time.Second, complete: true},
		{name: "late_completion_after_expiry", restartOffset: 90 * time.Second, complete: true},
		{name: "ordinary_expiry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store, manager := newBackupStore(t, root)
			now := time.Unix(1_800_000_000, 0).UTC()
			policy, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{
				RepositoryID: "project", Executor: state.CheckExecutorHost, AllowedEvents: []string{"push"},
				MaxTimeoutMS: 120000, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
			}, now)
			noErr(t, err)
			_, err = store.GrantCheckConsent(ctx, "project", now)
			noErr(t, err)
			repositoryPath, err := manager.Path("project")
			noErr(t, err)
			sourceOID := gitOutput(t, repositoryPath, "--git-dir", ".", "rev-parse", "refs/heads/main")
			checks := []state.CheckDefinition{{Name: "unit", Command: "go test ./..."}}
			admitted, deduped, err := store.AdmitCheckJob(ctx, state.CheckJobRequest{
				RepositoryID: "project", Trigger: "push", EventKey: "refs/heads/main@" + sourceOID,
				SourceOID: sourceOID, TriggerRef: "main", WorkflowDigest: strings.Repeat("b", 64), Checks: checks,
			}, now)
			if err != nil || deduped {
				t.Fatalf("admit: deduped=%v err=%v", deduped, err)
			}
			claimed, found, err := store.ClaimLocalCheckJob(ctx, "project", now.Add(time.Second))
			if err != nil || !found || claimed.ID != admitted.ID || claimed.LeaseExpiresAt == nil {
				t.Fatalf("claim: found=%v err=%v", found, err)
			}
			authority := state.CheckJobCompletionAuthority{JobID: claimed.ID, LeaseID: claimed.LeaseID,
				CredentialID: claimed.CredentialID, CredentialGeneration: claimed.CredentialGeneration}
			attempt := state.CheckAttempt{
				ID: "0123456789abcdef0123456789abcdef", RepositoryID: "project", TaskID: claimed.TaskID,
				JobID: claimed.ID, CredentialID: claimed.CredentialID, RevisionOID: sourceOID,
				WorktreeState: state.WorktreeClean, Checks: checks, StartedAt: now.Add(2 * time.Second), CreatedAt: now.Add(2 * time.Second),
			}
			start := state.CheckJobStart{RepositoryID: "project", JobID: claimed.ID, LeaseID: claimed.LeaseID,
				CredentialID: claimed.CredentialID, CredentialGeneration: claimed.CredentialGeneration, Protection: state.ProtectionHost,
				AttemptID: attempt.ID}
			completion := state.CheckCompletion{AttemptID: attempt.ID, RepositoryID: "project", TaskID: claimed.TaskID,
				Results:    []state.CheckResult{{Name: "unit", Command: "go test ./...", Status: state.AttemptFailed}},
				FinishedAt: now.Add(tc.restartOffset + time.Second), WorktreeState: state.WorktreeClean}
			_, err = store.RecoverySnapshot(ctx)
			noErr(t, err)
			if tc.preStartStatus != "" {
				_, err = store.FailCheckJobBeforeStart(ctx, authority, tc.preStartStatus, "Synthetic preflight did not start execution.", now.Add(3*time.Second))
				noErr(t, err)
				if _, _, err = store.StartCheckJob(ctx, start, now.Add(4*time.Second)); !errors.Is(err, state.ErrCheckJobState) {
					t.Fatalf("start after terminal preflight: %v", err)
				}
				if _, _, err = store.RegisterCheckAttempt(ctx, attempt); !errors.Is(err, state.ErrCheckJobState) {
					t.Fatalf("register after terminal preflight: %v", err)
				}
				_, err = store.CancelCheckJob(ctx, "project", claimed.ID, now.Add(4*time.Second))
				noErr(t, err)
			} else {
				if tc.legacy {
					noErr(t, store.Exec(ctx, `UPDATE check_jobs SET status='started',started_at=?,protection=? WHERE id=?`,
						attempt.StartedAt.UnixNano(), state.ProtectionHost, claimed.ID))
				} else {
					_, _, err = store.StartCheckJob(ctx, start, now.Add(2*time.Second))
					noErr(t, err)
				}
				_, err = store.RecoverySnapshot(ctx)
				noErr(t, err)
				var count int
				if tc.restartOffset != 0 {
					count, err = store.ReconcileCheckJobRestart(ctx, now.Add(tc.restartOffset))
				} else {
					count, err = store.ExpireCheckJobLeases(ctx, now.Add(90*time.Second))
				}
				if err != nil || count != 1 {
					t.Fatalf("lease-loss transition: count=%d err=%v", count, err)
				}
				if tc.complete {
					_, _, err = store.CompleteCheckJobAttempt(ctx, completion, authority, completion.FinishedAt)
					noErr(t, err)
				}
			}
			job, found, err := store.CheckJob(ctx, "project", claimed.ID)
			if err != nil || !found {
				t.Fatalf("read producer history: found=%v err=%v", found, err)
			}
			if job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.Equal(*claimed.LeaseExpiresAt) || job.LeaseID != claimed.LeaseID {
				t.Fatal("producer changed the original lease")
			}
			if tc.preStartStatus != "" {
				if job.Status != tc.preStartStatus || job.StartedAt != nil || job.AttemptID != "" || job.FinishedAt == nil ||
					!job.FinishedAt.Equal(now.Add(3*time.Second)) || job.Protection != state.ProtectionUnknown ||
					job.LeaseLostAt != nil || job.InterruptedAt != nil || job.CancelRequestedAt != nil {
					t.Fatal("pre-start producer changed the recorded outcome or fabricated execution")
				}
			} else {
				wantLoss, wantStatus := *claimed.LeaseExpiresAt, state.CheckJobAmbiguous
				if tc.restartOffset != 0 {
					wantLoss = now.Add(tc.restartOffset)
				}
				if tc.complete {
					wantStatus = state.CheckJobFailed
				}
				if job.Status != wantStatus || job.StartedAt == nil || !job.StartedAt.Equal(attempt.StartedAt) ||
					(job.AttemptID == "") != tc.legacy || (job.FinishedAt != nil) != tc.complete ||
					(tc.complete && !job.FinishedAt.Equal(completion.FinishedAt)) || job.LeaseLostAt == nil || !job.LeaseLostAt.Equal(wantLoss) {
					t.Fatal("lease-loss producer changed the original execution or observed loss time")
				}
			}
			snapshot, err := store.RecoverySnapshot(ctx)
			noErr(t, err)
			if len(snapshot.CheckJobs) != 1 {
				t.Fatalf("snapshot holds %d check jobs, want 1", len(snapshot.CheckJobs))
			}
			if difference := checkJobDifference(job, snapshot.CheckJobs[0]); difference != "" {
				t.Fatalf("snapshot changed producer history: %s", difference)
			}
			backup := filepath.Join(root, "backup")
			_, err = CreateWithReport(ctx, store, manager, backup)
			noErr(t, err)
			unchanged, found, err := store.CheckJob(ctx, "project", job.ID)
			if difference := checkJobDifference(job, unchanged); err != nil || !found || difference != "" {
				t.Fatalf("recovery calls changed stored job: found=%v err=%v difference=%s", found, err, difference)
			}
			content, err := os.ReadFile(filepath.Join(backup, manifestName))
			noErr(t, err)
			for _, field := range []string{"authority_epoch", "consent_active", "runner_credentials"} {
				if strings.Contains(string(content), `"`+field+`"`) {
					t.Fatalf("manifest contains machine-local field %s", field)
				}
			}
			manifest, err := readManifest(filepath.Join(backup, manifestName))
			noErr(t, err)
			portable := recoveryState(manifest)
			if len(portable.CheckJobs) != 1 {
				t.Fatalf("published backup holds %d check jobs, want 1", len(portable.CheckJobs))
			}
			if difference := checkJobDifference(job, portable.CheckJobs[0]); difference != "" {
				t.Fatalf("published backup changed producer history: %s", difference)
			}
			restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
			restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
			noErr(t, Restore(ctx, backup, restoredState, restoredRepositories, ""))
			restored, err := state.Open(ctx, restoredState)
			noErr(t, err)
			defer restored.Close()
			restoredJob, found, err := restored.CheckJob(ctx, "project", job.ID)
			if difference := checkJobDifference(job, restoredJob); err != nil || !found || difference != "" {
				t.Fatalf("restore changed terminal history: found=%v err=%v difference=%s", found, err, difference)
			}
			restoredPolicy, found, err := restored.CheckPolicy(ctx, "project")
			if err != nil || !found || restoredPolicy.ConsentActive || restoredPolicy.AuthorityEpoch == policy.AuthorityEpoch {
				t.Fatalf("restored execution authority: found=%v err=%v", found, err)
			}
			credentials, err := restored.CheckRunnerCredentials(ctx, "project")
			noErr(t, err)
			if len(credentials) != 0 {
				t.Fatal("restore retained runner credentials")
			}
			if _, _, err = restored.StartCheckJob(ctx, start, now.Add(100*time.Second)); err == nil {
				t.Fatal("restore granted execution for terminal history")
			}
			if tc.preStartStatus == "" && !tc.legacy && !tc.complete {
				completion.FinishedAt = now.Add(100 * time.Second)
				if _, _, err = restored.CompleteCheckJobAttempt(ctx, completion, authority, completion.FinishedAt); !errors.Is(err, state.ErrCheckRunnerCredential) {
					t.Fatalf("restored pending completion authority: %v", err)
				}
			}
			restoredManager := &repository.Manager{Store: restored, Git: restoredRunner(t, restoredState), Locks: gitexec.NewLocks(), Root: restoredRepositories}
			rebackup := filepath.Join(root, "rebackup")
			_, err = CreateWithReport(ctx, restored, restoredManager, rebackup)
			noErr(t, err)
			rebacked, err := readManifest(filepath.Join(rebackup, manifestName))
			noErr(t, err)
			if !reflect.DeepEqual(rebacked.CheckJobs, manifest.CheckJobs) || !reflect.DeepEqual(rebacked.CheckAttempts, manifest.CheckAttempts) ||
				!reflect.DeepEqual(rebacked.CheckPolicies, manifest.CheckPolicies) {
				t.Fatal("rebackup changed portable check facts")
			}
		})
	}
}

// checkJobDifference names the first field whose recorded fact differs, or
// returns "". A backup may write an instant with another zone offset, so every
// time is compared as an instant and an absent optional time must stay absent.
// Every other field, including one added later, must be exactly equal.
func checkJobDifference(want, got state.CheckJob) string {
	wantFields, gotFields := reflect.ValueOf(want), reflect.ValueOf(got)
	for i := range wantFields.NumField() {
		wantField, gotField := wantFields.Field(i).Interface(), gotFields.Field(i).Interface()
		if !sameFact(wantField, gotField) {
			return fmt.Sprintf("%s: want %v, got %v", wantFields.Type().Field(i).Name, wantField, gotField)
		}
	}
	return ""
}

func sameFact(want, got any) bool {
	switch want := want.(type) {
	case time.Time:
		return want.Equal(got.(time.Time))
	case *time.Time:
		got := got.(*time.Time)
		return want == got || want != nil && got != nil && want.Equal(*got)
	}
	return reflect.DeepEqual(want, got)
}
