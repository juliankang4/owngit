package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

// jobRecoveryFixture builds a complete portable snapshot: a policy, a passed
// job with a server-linked attempt, and one pending job.
type jobRecoveryFixture struct {
	snapshot    RecoveryState
	policyID    string
	terminalID  string
	pendingID   string
	attemptID   string
	runnerToken string
	runnerID    string
	consent     int64
	runnerGen   int64
	authority   string
	now         time.Time
}

func newJobRecoveryFixture(t *testing.T) jobRecoveryFixture {
	t.Helper()
	fixture := newCheckJobFixture(t)
	policy := fixture.setPolicy(t, nil)
	granted := fixture.grantConsent(t)
	runner, token := fixture.issueRunner(t)
	ctx := context.Background()

	terminal := fixture.admit(t, pushJobRequest())
	claimed, _, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: terminal.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now); err != nil {
		t.Fatal(err)
	}
	attempt := fixture.registerJobAttempt(t, claimed, runner)
	completeJobAttempt(t, fixture.store, attempt, AttemptPassed, fixture.now.Add(time.Minute))

	request := pushJobRequest()
	request.EventKey = "refs/heads/main@" + strings.Repeat("f", 40)
	request.SourceOID = strings.Repeat("f", 40)
	pending := fixture.admit(t, request)
	if err := fixture.store.RecordCheckObservation(ctx, "project", "refs/heads/main", pending.SourceOID, fixture.now); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.store.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return jobRecoveryFixture{
		snapshot: snapshot, policyID: policy.RepositoryID, terminalID: terminal.ID, pendingID: pending.ID,
		attemptID: attempt.ID, runnerToken: token, runnerID: runner.ID,
		consent: granted.ConsentVersion, runnerGen: runner.Generation, authority: policy.AuthorityEpoch,
		now: fixture.now,
	}
}

func cloneJobRecoveryState(snapshot RecoveryState) RecoveryState {
	clone := snapshot
	clone.Repositories = append([]Repository(nil), snapshot.Repositories...)
	clone.CheckPolicies = append([]CheckPolicy(nil), snapshot.CheckPolicies...)
	clone.CheckJobs = append([]CheckJob(nil), snapshot.CheckJobs...)
	clone.Tasks = append([]RecoveryTask(nil), snapshot.Tasks...)
	clone.CheckConfigurations = append([]CheckConfiguration(nil), snapshot.CheckConfigurations...)
	clone.CheckAttempts = append([]CheckAttempt(nil), snapshot.CheckAttempts...)
	return clone
}

func recoveryJobIndex(t *testing.T, snapshot RecoveryState, id string) int {
	t.Helper()
	for index := range snapshot.CheckJobs {
		if snapshot.CheckJobs[index].ID == id {
			return index
		}
	}
	t.Fatalf("job %q is absent", id)
	return -1
}

func recoveryPolicyIndex(t *testing.T, snapshot RecoveryState, repositoryID string) int {
	t.Helper()
	for index := range snapshot.CheckPolicies {
		if snapshot.CheckPolicies[index].RepositoryID == repositoryID {
			return index
		}
	}
	t.Fatalf("policy %q is absent", repositoryID)
	return -1
}

func recoveryAttemptIndexByJob(t *testing.T, snapshot RecoveryState, jobID string) int {
	t.Helper()
	for index := range snapshot.CheckAttempts {
		if snapshot.CheckAttempts[index].JobID == jobID {
			return index
		}
	}
	t.Fatalf("attempt for job %q is absent", jobID)
	return -1
}

func TestRestoreInvalidatesCheckExecutionAuthority(t *testing.T) {
	fixture := newJobRecoveryFixture(t)
	ctx := context.Background()
	// Seed local-only authority that the restore must delete.
	destination := openTestStore(t)
	localHash := sha256.Sum256([]byte("local-token"))
	exec := func(query string, arguments ...any) {
		t.Helper()
		if _, err := destination.db.ExecContext(ctx, query, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`PRAGMA foreign_keys=OFF`)
	exec(`INSERT INTO check_runner_credentials(id,repository_id,label,creation_id,generation,token_hash,created_at) VALUES(?,?,?,?,?,?,?)`,
		strings.Repeat("a", 32), "project", "local", "", 1, localHash[:], fixture.now.Unix())
	exec(`INSERT INTO check_observations(repository_id,ref_name,oid,observed_at) VALUES(?,?,?,?)`,
		"project", "refs/heads/old", strings.Repeat("e", 40), fixture.now.Unix())
	exec(`PRAGMA foreign_keys=ON`)
	if err := destination.RestoreRecoveryState(ctx, t.TempDir(), cloneJobRecoveryState(fixture.snapshot)); err != nil {
		t.Fatal(err)
	}

	policy, exists, err := destination.CheckPolicy(ctx, "project")
	if err != nil || !exists {
		t.Fatalf("restored policy exists=%v err=%v", exists, err)
	}
	if policy.ConsentActive || policy.ConsentVersion != fixture.consent || policy.RunnerGeneration != fixture.runnerGen {
		t.Fatalf("restored policy consent=%v version=%d generation=%d", policy.ConsentActive, policy.ConsentVersion, policy.RunnerGeneration)
	}
	if policy.AuthorityEpoch == fixture.authority || policy.Digest == "" {
		t.Fatalf("restored authority epoch=%q", policy.AuthorityEpoch)
	}
	// Terminal history survives byte-for-byte, including the job link.
	terminal, exists, err := destination.CheckJob(ctx, "project", fixture.terminalID)
	if err != nil || !exists || terminal.Status != CheckJobPassed {
		t.Fatalf("restored terminal job=%+v exists=%v err=%v", terminal, exists, err)
	}
	attempt, exists, err := destination.CheckAttemptByID(ctx, "project", fixture.attemptID)
	if err != nil || !exists || attempt.JobID != fixture.terminalID || !validDigest(attempt.RegistrationDigest) {
		t.Fatalf("restored attempt job=%q digest=%q exists=%v err=%v", attempt.JobID, attempt.RegistrationDigest, exists, err)
	}
	// Unfinished work is interrupted with cleared lease authority.
	pending, _, err := destination.CheckJob(ctx, "project", fixture.pendingID)
	if err != nil || pending.Status != CheckJobInterrupted || pending.InterruptedAt == nil || pending.FinishedAt == nil || pending.LeaseID != "" || pending.LeaseExpiresAt != nil {
		t.Fatalf("restored pending job=%+v err=%v", pending, err)
	}
	// Runner credentials, observations, and authority are machine-local.
	if credentials, err := destination.CheckRunnerCredentials(ctx, "project"); err != nil || len(credentials) != 0 {
		t.Fatalf("restored credentials=%+v err=%v", credentials, err)
	}
	if _, found, err := destination.RunnerCredentialByToken(ctx, "project", "local-token", fixture.now); err != nil || found {
		t.Fatalf("local token survived restore found=%v err=%v", found, err)
	}
	if _, found, err := destination.RunnerCredentialByToken(ctx, "project", fixture.runnerToken, fixture.now); err != nil || found {
		t.Fatalf("old token found=%v err=%v", found, err)
	}
	if _, _, err := destination.ClaimCheckJob(ctx, "project", fixture.runnerID, fixture.now); !errors.Is(err, ErrCheckRunnerCredential) {
		t.Fatalf("restored claim error=%v", err)
	}
	if observations, err := destination.CheckObservations(ctx, "project"); err != nil || len(observations) != 0 {
		t.Fatalf("restored observations=%+v err=%v", observations, err)
	}
	// Fresh issuance uses the next generation and cannot revive the old bearer.
	next, nextToken, created, err := destination.IssueCheckRunnerToken(ctx, "project", "server", "", fixture.now)
	if err != nil || !created || next.Generation != fixture.runnerGen+1 || nextToken == "" {
		t.Fatalf("reissued token generation=%d created=%v err=%v", next.Generation, created, err)
	}
	if _, found, err := destination.RunnerCredentialByToken(ctx, "project", fixture.runnerToken, fixture.now); err != nil || found {
		t.Fatalf("old token revived after fresh issuance found=%v err=%v", found, err)
	}
	// Nothing claims until consent is regranted, and the dedup still finds the
	// interrupted job instead of silently creating a new one.
	if _, claimed, err := destination.ClaimCheckJob(ctx, "project", next.ID, fixture.now); err != nil || claimed {
		t.Fatalf("claim before consent claimed=%v err=%v", claimed, err)
	}
	if _, err := destination.GrantCheckConsent(ctx, "project", fixture.now); err != nil {
		t.Fatal(err)
	}
	replay := pushJobRequest()
	replay.EventKey = "refs/heads/main@" + strings.Repeat("f", 40)
	replay.SourceOID = strings.Repeat("f", 40)
	// Regranting consent creates a new consent generation, so the fresh
	// observation is admitted as a new job while the interrupted evidence stays
	// terminal until an explicit rerun.
	reobserved, deduped, err := destination.AdmitCheckJob(ctx, replay, fixture.now)
	if err != nil || deduped || reobserved.ID == fixture.pendingID || reobserved.Status != CheckJobPending {
		t.Fatalf("post-restore admission id=%q deduped=%v status=%s err=%v", reobserved.ID, deduped, reobserved.Status, err)
	}
	stillInterrupted, _, err := destination.CheckJob(ctx, "project", fixture.pendingID)
	if err != nil || stillInterrupted.Status != CheckJobInterrupted {
		t.Fatalf("interrupted evidence=%+v err=%v", stillInterrupted, err)
	}
	rerun, deduped, err := destination.RerunCheckJob(ctx, "project", fixture.pendingID, fixture.now)
	if err != nil || deduped || rerun.Status != CheckJobPending {
		t.Fatalf("post-restore rerun=%+v deduped=%v err=%v", rerun, deduped, err)
	}
	if _, claimed, err := destination.ClaimCheckJob(ctx, "project", next.ID, fixture.now); err != nil || !claimed {
		t.Fatalf("rerun claim claimed=%v err=%v", claimed, err)
	}
	rebacked, err := destination.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatalf("re-backup validation: %v", err)
	}
	if len(rebacked.CheckJobs) != len(fixture.snapshot.CheckJobs)+2 {
		t.Fatalf("rebacked jobs=%d original=%d", len(rebacked.CheckJobs), len(fixture.snapshot.CheckJobs))
	}
}

func TestRestoreInterruptedJobRejectsLateCompletion(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	job := fixture.admit(t, pushJobRequest())
	runner, _ := fixture.issueRunner(t)
	ctx := context.Background()
	claimed, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now); err != nil {
		t.Fatal(err)
	}
	attempt := fixture.registerJobAttempt(t, claimed, runner)
	snapshot, err := fixture.store.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored := openTestStore(t)
	if err := restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot); err != nil {
		t.Fatal(err)
	}
	exit := 0
	completion := CheckCompletion{
		AttemptID: attempt.ID, RepositoryID: attempt.RepositoryID, TaskID: attempt.TaskID,
		Results:    []CheckResult{{Name: "unit", Command: "go test ./...", Status: AttemptPassed, ExitCode: &exit, DurationMS: 1}},
		FinishedAt: fixture.now.Add(time.Second), WorktreeState: WorktreeClean,
	}
	if _, _, err := restored.CompleteCheckAttempt(ctx, completion, fixture.now.Add(time.Second)); !errors.Is(err, ErrCheckJobCompletionRequired) {
		t.Fatalf("ordinary restored completion error=%v", err)
	}
	authority := CheckJobCompletionAuthority{
		JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}
	if _, _, err := restored.CompleteCheckJobAttempt(ctx, completion, authority, fixture.now.Add(time.Second)); err == nil {
		t.Fatal("restored claim authority completed interrupted work")
	}
	interrupted, found, err := restored.CheckJob(ctx, "project", job.ID)
	if err != nil || !found || interrupted.Status != CheckJobInterrupted {
		t.Fatalf("restored job=%+v found=%v err=%v", interrupted, found, err)
	}
	storedAttempt, found, err := restored.CheckAttemptByID(ctx, "project", attempt.ID)
	if err != nil || !found || storedAttempt.Status != AttemptPending {
		t.Fatalf("restored attempt=%+v found=%v err=%v", storedAttempt, found, err)
	}
}

func TestCheckRecoveryRejectsInvalidJobRelationships(t *testing.T) {
	fixture := newJobRecoveryFixture(t)
	terminal := recoveryJobIndex(t, fixture.snapshot, fixture.terminalID)
	pending := recoveryJobIndex(t, fixture.snapshot, fixture.pendingID)
	policy := recoveryPolicyIndex(t, fixture.snapshot, "project")
	attempt := recoveryAttemptIndexByJob(t, fixture.snapshot, fixture.terminalID)
	zero := strings.Repeat("0", 64)

	tests := []struct {
		name   string
		mutate func(*RecoveryState)
	}{
		{"unknown job", func(snapshot *RecoveryState) { snapshot.CheckAttempts[attempt].JobID = strings.Repeat("9", 32) }},
		{"orphan job attempt", func(snapshot *RecoveryState) { snapshot.CheckJobs[terminal].AttemptID = strings.Repeat("9", 32) }},
		{"asymmetric attempt binding", func(snapshot *RecoveryState) {
			snapshot.CheckJobs[terminal].AttemptID = snapshot.CheckAttempts[attempt].ID
			snapshot.CheckAttempts[attempt].JobID = snapshot.CheckJobs[pending].ID
		}},
		{"attempt-only job link", func(snapshot *RecoveryState) {
			job := &snapshot.CheckJobs[terminal]
			job.Status = CheckJobInterrupted
			job.LeaseID = ""
			job.LeaseExpiresAt = nil
			job.InterruptedAt = job.FinishedAt
			job.AttemptID = ""
		}},
		{"terminal job missing credential", func(snapshot *RecoveryState) {
			snapshot.CheckJobs[terminal].CredentialID = ""
			snapshot.CheckJobs[terminal].CredentialGeneration = 0
		}},
		{"attempt repository mismatch", func(snapshot *RecoveryState) {
			snapshot.CheckAttempts[attempt].RepositoryID = "other"
			snapshot.CheckJobs[pending].RepositoryID = "project"
		}},
		{"dedup digest", func(snapshot *RecoveryState) { snapshot.CheckJobs[terminal].DedupDigest = zero }},
		{"limits out of range", func(snapshot *RecoveryState) { snapshot.CheckJobs[terminal].Limits.TimeoutMS = 1 }},
		{"policy generation above policy", func(snapshot *RecoveryState) { snapshot.CheckJobs[terminal].PolicyVersion = 99 }},
		{"current policy executor mismatch", func(snapshot *RecoveryState) {
			job := &snapshot.CheckJobs[terminal]
			job.Executor = CheckExecutorContainer
			job.DedupDigest = checkJobDedupDigest(*job, snapshot.CheckConfigurations[0].ConfigHash)
			snapshot.CheckAttempts[attempt].ExecutionScope = ExecutionScopeContainer
			recomputed := snapshot.CheckAttempts[attempt]
			recomputed.Checks = snapshot.CheckConfigurations[0].Checks
			snapshot.CheckAttempts[attempt].RegistrationDigest = registrationDigest(recomputed)
		}},
		{"consent generation above policy", func(snapshot *RecoveryState) { snapshot.CheckJobs[terminal].ConsentVersion = 99 }},
		{"push job with pull request facts", func(snapshot *RecoveryState) { snapshot.CheckJobs[terminal].PullRequestNumber = 1 }},
		{"pending job with claim", func(snapshot *RecoveryState) {
			when := snapshot.CheckJobs[pending].AdmittedAt
			snapshot.CheckJobs[pending].ClaimedAt = &when
		}},
		{"policy digest", func(snapshot *RecoveryState) { snapshot.CheckPolicies[policy].Digest = zero }},
		{"invalid consent digest", func(snapshot *RecoveryState) {
			snapshot.CheckPolicies[policy].ConsentVersion = 1
			snapshot.CheckPolicies[policy].ConsentDigest = "short"
		}},
		{"duplicate dedup digest", func(snapshot *RecoveryState) {
			duplicate := snapshot.CheckJobs[terminal]
			duplicate.ID = strings.Repeat("8", 32)
			snapshot.CheckJobs = append(snapshot.CheckJobs, duplicate)
		}},
		{"unknown rerun root", func(snapshot *RecoveryState) {
			snapshot.CheckJobs[terminal].RerunRoot = strings.Repeat("7", 32)
			snapshot.CheckJobs[terminal].RerunGeneration = 1
		}},
		{"unknown configuration", func(snapshot *RecoveryState) { snapshot.CheckJobs[terminal].ConfigurationVersion = 99 }},
		{"unknown job task", func(snapshot *RecoveryState) { snapshot.CheckJobs[terminal].TaskID = strings.Repeat("7", 32) }},
		{"job task does not match trigger", func(snapshot *RecoveryState) {
			other := snapshot.Tasks[0]
			other.ID = strings.Repeat("8", 32)
			snapshot.Tasks = append(snapshot.Tasks, other)
			job := &snapshot.CheckJobs[terminal]
			job.TaskID = other.ID
			job.DedupDigest = checkJobDedupDigest(*job, snapshot.CheckConfigurations[0].ConfigHash)
			snapshot.CheckAttempts[attempt].TaskID = other.ID
			recomputed := snapshot.CheckAttempts[attempt]
			recomputed.Checks = snapshot.CheckConfigurations[0].Checks
			snapshot.CheckAttempts[attempt].RegistrationDigest = registrationDigest(recomputed)
		}},
		{"attempt task mismatch", func(snapshot *RecoveryState) {
			other := snapshot.Tasks[0]
			other.ID = strings.Repeat("8", 32)
			snapshot.Tasks = append(snapshot.Tasks, other)
			snapshot.CheckAttempts[attempt].TaskID = other.ID
		}},
		{"attempt revision mismatch", func(snapshot *RecoveryState) { snapshot.CheckAttempts[attempt].RevisionOID = strings.Repeat("7", 40) }},
		{"attempt limits mismatch", func(snapshot *RecoveryState) { snapshot.CheckAttempts[attempt].TimeoutMS++ }},
		{"protection mismatch", func(snapshot *RecoveryState) { snapshot.CheckAttempts[attempt].Protection = ProtectionContainer }},
		{"scope mismatch", func(snapshot *RecoveryState) {
			snapshot.CheckAttempts[attempt].ExecutionScope = ExecutionScopeContainer
		}},
		{"unknown attempt job", func(snapshot *RecoveryState) { snapshot.CheckAttempts[attempt].JobID = zero[:32] }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := cloneJobRecoveryState(fixture.snapshot)
			test.mutate(&snapshot)
			if err := ValidateCheckRecovery(snapshot); err == nil {
				t.Fatalf("accepted invalid check recovery: %+v", snapshot.CheckJobs)
			}
		})
	}
	if err := ValidateCheckRecovery(fixture.snapshot); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
}

func TestRestoreLeavesDestinationUntouchedOnInvalidJobs(t *testing.T) {
	fixture := newJobRecoveryFixture(t)
	invalid := cloneJobRecoveryState(fixture.snapshot)
	index := recoveryJobIndex(t, invalid, fixture.terminalID)
	invalid.CheckJobs[index].DedupDigest = strings.Repeat("0", 64)
	destination := openTestStore(t)
	if err := destination.RestoreRecoveryState(context.Background(), t.TempDir(), invalid); err == nil {
		t.Fatal("restored invalid job state")
	}
	if _, exists, err := destination.CheckJob(context.Background(), "project", fixture.terminalID); err != nil || exists {
		t.Fatalf("invalid restore wrote job exists=%v err=%v", exists, err)
	}
}
