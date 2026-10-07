package recovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// TestBackupRoundTripsJobsAndInvalidatesAuthority covers the portable
// automatic-check facts and the machine-local authority a restore invalidates.
func TestBackupRoundTripsJobsAndInvalidatesAuthority(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	now := time.Unix(1_800_000_000, 0)
	repositoryPath, _, exists, err := manager.ExistingPath(ctx, "project")
	if err != nil || !exists {
		t.Fatalf("repository exists=%v err=%v", exists, err)
	}
	sourceOID := strings.TrimSpace(gitOutput(t, repositoryPath, "--git-dir", ".", "rev-parse", "refs/heads/main"))

	policy, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push", "pull_request"},
		MaxTimeoutMS: 120000, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
	}, now)
	noErr(t, err)
	if _, err := store.GrantCheckConsent(ctx, "project", now); err != nil {
		t.Fatal(err)
	}
	runner, rawToken, created, err := store.IssueCheckRunnerToken(ctx, "project", "runner", "", now)
	if err != nil || !created || rawToken == "" {
		t.Fatalf("issue runner created=%v err=%v", created, err)
	}
	request := state.CheckJobRequest{
		RepositoryID: "project", Trigger: "push", EventKey: "refs/heads/main@" + sourceOID,
		SourceOID: sourceOID, TriggerRef: "main", WorkflowDigest: strings.Repeat("b", 64),
		Checks: []state.CheckDefinition{{Name: "unit", Command: "go test ./..."}},
	}
	job, deduped, err := store.AdmitCheckJob(ctx, request, now)
	if err != nil || deduped {
		t.Fatalf("admit deduped=%v err=%v", deduped, err)
	}
	claimed, _, err := store.ClaimCheckJob(ctx, "project", runner.ID, now)
	noErr(t, err)
	_, attempt, err := store.StartCheckJob(ctx, state.CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation, AttemptID: "0123456789abcdef0123456789abcdef",
	}, now)
	if err != nil {
		t.Fatal(err)
	} else if attempt.ExecutionScope != state.ExecutionScopeExternalRunner {
		t.Fatalf("derived scope=%q", attempt.ExecutionScope)
	}
	exit := 0
	if _, _, err := store.CompleteCheckJobAttempt(ctx, state.CheckCompletion{
		AttemptID: attempt.ID, RepositoryID: "project", TaskID: job.TaskID,
		Results: []state.CheckResult{{
			Name: "unit", Command: "go test ./...", Status: state.AttemptPassed, ExitCode: &exit, DurationMS: 5,
		}},
		FinishedAt: now.Add(time.Second), WorktreeState: state.WorktreeClean, Log: "log",
	}, state.CheckJobCompletionAuthority{
		JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	pendingRequest := request
	pendingRequest.EventKey = "refs/heads/main@" + strings.Repeat("d", 40)
	pendingRequest.SourceOID = strings.Repeat("d", 40)
	pending, deduped, err := store.AdmitCheckJob(ctx, pendingRequest, now.Add(2*time.Second))
	if err != nil || deduped {
		t.Fatalf("second admission deduped=%v err=%v", deduped, err)
	}
	noErr(t, store.RecordCheckObservation(ctx, "project", "refs/heads/main", sourceOID, now))

	backup := filepath.Join(root, "backup")
	_, err = CreateWithReport(ctx, store, manager, backup)
	noErr(t, err)
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	if manifest.Version != closedPullRequestBackupVersion || len(manifest.CheckPolicies) != 1 || len(manifest.CheckJobs) != 2 {
		t.Fatalf("backup version=%d policies=%d jobs=%d", manifest.Version, len(manifest.CheckPolicies), len(manifest.CheckJobs))
	}
	if manifest.CheckPolicies[0].PolicyVersion != policy.Version || manifest.CheckPolicies[0].RunnerGeneration != 1 {
		t.Fatalf("backup policy=%+v", manifest.CheckPolicies[0])
	}
	var terminal CheckJobManifest
	for _, job := range manifest.CheckJobs {
		if job.AttemptID == attempt.ID {
			terminal = job
		}
	}
	if terminal.ID == "" || terminal.TaskID != job.TaskID || terminal.Status != state.CheckJobPassed || terminal.AttemptID == "" {
		t.Fatalf("backup terminal job=%+v", terminal)
	}
	content, err := os.ReadFile(filepath.Join(backup, manifestName))
	noErr(t, err)
	if strings.Contains(string(content), rawToken) {
		t.Fatal("backup manifest contains the raw runner token")
	}

	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
	if _, err := RestoreWithReport(ctx, backup, restoredState, restoredRepositories, ""); err != nil {
		t.Fatal(err)
	}
	restored, err := state.Open(ctx, restoredState)
	noErr(t, err)
	defer restored.Close()
	restoredPolicy, exists, err := restored.CheckPolicy(ctx, "project")
	if err != nil || !exists || restoredPolicy.Version != policy.Version || restoredPolicy.ConsentActive || restoredPolicy.RunnerGeneration != 1 {
		t.Fatalf("restored policy=%+v exists=%v err=%v", restoredPolicy, exists, err)
	}
	if restoredPolicy.AuthorityEpoch == policy.AuthorityEpoch {
		t.Fatal("restore reused the authority epoch")
	}
	restoredTerminal, exists, err := restored.CheckJob(ctx, "project", terminal.ID)
	if err != nil || !exists || restoredTerminal.Status != state.CheckJobPassed || restoredTerminal.FinishedAt == nil {
		t.Fatalf("restored terminal job=%+v exists=%v err=%v", restoredTerminal, exists, err)
	}
	restoredPending, _, err := restored.CheckJob(ctx, "project", pending.ID)
	if err != nil || restoredPending.Status != state.CheckJobInterrupted || restoredPending.LeaseID != "" {
		t.Fatalf("restored pending job=%+v err=%v", restoredPending, err)
	}
	restoredAttempt, exists, err := restored.CheckAttemptByID(ctx, "project", attempt.ID)
	if err != nil || !exists || restoredAttempt.JobID != terminal.ID || restoredAttempt.RegistrationDigest != manifest.CheckAttempts[0].RegistrationDigest {
		t.Fatalf("restored attempt job=%q exists=%v err=%v", restoredAttempt.JobID, exists, err)
	}
	if _, found, err := restored.RunnerCredentialByToken(ctx, "project", rawToken, now); err != nil || found {
		t.Fatalf("restored token found=%v err=%v", found, err)
	}
	if observations, err := restored.CheckObservations(ctx, "project"); err != nil || len(observations) != 0 {
		t.Fatalf("restored observations=%+v err=%v", observations, err)
	}
	next, nextToken, created, err := restored.IssueCheckRunnerToken(ctx, "project", "runner", "", now)
	if err != nil || !created || next.Generation != 2 || nextToken == "" {
		t.Fatalf("reissued generation=%d created=%v err=%v", next.Generation, created, err)
	}
	if _, found, err := restored.RunnerCredentialByToken(ctx, "project", rawToken, now); err != nil || found {
		t.Fatalf("old restored token revived found=%v err=%v", found, err)
	}

	restoredManager := &repository.Manager{Store: restored, Git: restoredRunner(t, restoredState), Locks: gitexec.NewLocks(), Root: restoredRepositories}
	rebackup := filepath.Join(root, "rebackup")
	_, err = CreateWithReport(ctx, restored, restoredManager, rebackup)
	noErr(t, err)
	rebacked, err := readManifest(filepath.Join(rebackup, manifestName))
	noErr(t, err)
	if rebacked.Version != closedPullRequestBackupVersion || len(rebacked.CheckJobs) != 2 {
		t.Fatalf("rebackup version=%d jobs=%d", rebacked.Version, len(rebacked.CheckJobs))
	}
}

func restoredRunner(t *testing.T, stateRoot string) *gitexec.Runner {
	t.Helper()
	runner, err := gitexec.New("", filepath.Join(stateRoot, "runtime"))
	noErr(t, err)
	return runner
}

// An installation whose runner's clock was behind kept job finishes, and
// cancellations that completions recorded, from before the job started.
// Its backup is made, verified and restored.
func TestBackupKeepsJobsFinishedByARunnerWithAnEarlierClock(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	now := time.Unix(1_800_000_000, 0)
	repositoryPath, _, exists, err := manager.ExistingPath(ctx, "project")
	if err != nil || !exists {
		t.Fatalf("repository exists=%v err=%v", exists, err)
	}
	sourceOID := strings.TrimSpace(gitOutput(t, repositoryPath, "--git-dir", ".", "rev-parse", "refs/heads/main"))
	_, err = store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "project", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"},
		MaxTimeoutMS: 120000, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
	}, now)
	noErr(t, err)
	_, err = store.GrantCheckConsent(ctx, "project", now)
	noErr(t, err)
	runner, _, _, err := store.IssueCheckRunnerToken(ctx, "project", "runner", "", now)
	noErr(t, err)
	behind := now.Add(-time.Hour)
	jobs := []string{}
	for index, cancelled := range []bool{false, true} {
		request := state.CheckJobRequest{
			RepositoryID: "project", Trigger: "push", EventKey: "refs/heads/main@" + sourceOID + strings.Repeat("x", index),
			SourceOID: sourceOID, TriggerRef: "main", WorkflowDigest: strings.Repeat("b", 64),
			Checks: []state.CheckDefinition{{Name: "unit", Command: "go test ./..."}},
		}
		job, _, err := store.AdmitCheckJob(ctx, request, now)
		noErr(t, err)
		claimed, _, err := store.ClaimCheckJob(ctx, "project", runner.ID, now)
		noErr(t, err)
		_, attempt, err := store.StartCheckJob(ctx, state.CheckJobStart{
			RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
			CredentialID: runner.ID, CredentialGeneration: runner.Generation, AttemptID: strings.Repeat(string(rune('a'+index)), 32),
		}, now)
		noErr(t, err)
		exit := 1
		_, _, err = store.CompleteCheckJobAttempt(ctx, state.CheckCompletion{
			AttemptID: attempt.ID, RepositoryID: "project", TaskID: job.TaskID, Cancelled: cancelled,
			Results:    []state.CheckResult{{Name: "unit", Command: "go test ./...", Status: state.AttemptFailed, ExitCode: &exit, DurationMS: 5}},
			FinishedAt: behind, WorktreeState: state.WorktreeClean, Log: "log",
		}, state.CheckJobCompletionAuthority{
			JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation,
		}, now.Add(time.Second))
		noErr(t, err)
		// What an earlier release recorded: the runner's time as the job's
		// finish, and as its cancellation when the completion cancelled it.
		noErr(t, store.Exec(ctx, `UPDATE check_jobs SET finished_at=? WHERE id=?`, behind.UnixNano(), job.ID))
		if cancelled {
			noErr(t, store.Exec(ctx, `UPDATE check_jobs SET cancel_requested_at=? WHERE id=?`, behind.UnixNano(), job.ID))
		}
		jobs = append(jobs, job.ID)
	}

	backup := filepath.Join(root, "backup")
	_, err = CreateWithReport(ctx, store, manager, backup)
	noErr(t, err)
	temporary := filepath.Join(root, "temporary")
	noErr(t, os.Mkdir(temporary, 0o700))
	result, err := Verify(ctx, backup, temporary, "")
	if err != nil || !result.Verified {
		t.Fatalf("verify %+v err=%v", result, err)
	}
	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
	if _, err := RestoreWithReport(ctx, backup, restoredState, restoredRepositories, ""); err != nil {
		t.Fatal(err)
	}
	restored, err := state.Open(ctx, restoredState)
	noErr(t, err)
	defer restored.Close()
	for _, id := range jobs {
		job, exists, err := restored.CheckJob(ctx, "project", id)
		if err != nil || !exists || job.FinishedAt == nil || !job.FinishedAt.Equal(behind) {
			t.Fatalf("restored job %+v exists=%v err=%v", job, exists, err)
		}
	}
}
