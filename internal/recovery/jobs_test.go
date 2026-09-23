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
	if _, err := store.StartCheckJob(ctx, state.CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, now); err != nil {
		t.Fatal(err)
	}
	attempt := state.CheckAttempt{
		ID: "0123456789abcdef0123456789abcdef", TaskID: job.TaskID, RepositoryID: "project",
		RevisionOID: sourceOID, WorktreeState: state.WorktreeClean, StartedAt: now, CreatedAt: now,
		JobID: job.ID, CredentialID: runner.ID,
		Checks: []state.CheckDefinition{{Name: "unit", Command: "go test ./..."}},
	}
	if _, stored, err := store.RegisterCheckAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	} else if stored.ExecutionScope != state.ExecutionScopeExternalRunner {
		t.Fatalf("derived scope=%q", stored.ExecutionScope)
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
	noErr(t, Create(ctx, store, manager, backup))
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	if manifest.Version != backupVersion || len(manifest.CheckPolicies) != 1 || len(manifest.CheckJobs) != 2 {
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
	noErr(t, Restore(ctx, backup, restoredState, restoredRepositories, ""))
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
	noErr(t, Create(ctx, restored, restoredManager, rebackup))
	rebacked, err := readManifest(filepath.Join(rebackup, manifestName))
	noErr(t, err)
	if rebacked.Version != backupVersion || len(rebacked.CheckJobs) != 2 {
		t.Fatalf("rebackup version=%d jobs=%d", rebacked.Version, len(rebacked.CheckJobs))
	}
}

func restoredRunner(t *testing.T, stateRoot string) *gitexec.Runner {
	t.Helper()
	runner, err := gitexec.New("", filepath.Join(stateRoot, "runtime"))
	noErr(t, err)
	return runner
}
