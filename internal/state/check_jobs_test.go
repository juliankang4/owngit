package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/checkworkflow"
)

type checkJobFixture struct {
	store *Store
	now   time.Time
}

func newCheckJobFixture(t *testing.T) *checkJobFixture {
	t.Helper()
	store := openTestStore(t)
	completeTestSetup(t, store)
	now := time.Unix(1_800_000_000, 0)
	if err := store.AddRepository(context.Background(), Repository{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(context.Background(), Repository{ID: "other", Name: "Other", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return &checkJobFixture{store: store, now: now}
}

func defaultPolicyInput() CheckPolicyInput {
	return CheckPolicyInput{
		RepositoryID: "project", Executor: CheckExecutorExternalRunner,
		AllowedEvents: []string{"push", "pull_request"},
		MaxTimeoutMS:  600000, MaxOutputLimitBytes: 65536,
		QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
	}
}

func (fixture *checkJobFixture) setPolicy(t *testing.T, mutate func(*CheckPolicyInput)) CheckPolicy {
	t.Helper()
	input := defaultPolicyInput()
	if mutate != nil {
		mutate(&input)
	}
	policy, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)
	if err != nil {
		t.Fatalf("set policy: %v", err)
	}
	return policy
}

func (fixture *checkJobFixture) grantConsent(t *testing.T) CheckPolicy {
	t.Helper()
	policy, err := fixture.store.GrantCheckConsent(context.Background(), "project", fixture.now)
	if err != nil {
		t.Fatalf("grant consent: %v", err)
	}
	return policy
}

func (fixture *checkJobFixture) issueRunner(t *testing.T) (RunnerCredential, string) {
	t.Helper()
	credential, token, created, err := fixture.store.IssueCheckRunnerToken(context.Background(), "project", "laptop", "", fixture.now)
	if err != nil || !created || token == "" {
		t.Fatalf("issue runner token created=%v err=%v", created, err)
	}
	return credential, token
}

func pushJobRequest() CheckJobRequest {
	return CheckJobRequest{
		RepositoryID: "project", Trigger: "push", EventKey: "refs/heads/main@" + strings.Repeat("a", 40),
		SourceOID: strings.Repeat("a", 40), TriggerRef: "main",
		WorkflowDigest: strings.Repeat("b", 64),
		Checks:         []CheckDefinition{{Name: "unit", Command: "go test ./..."}},
	}
}

func pullRequestJobRequest() CheckJobRequest {
	request := pushJobRequest()
	request.Trigger = "pull_request"
	request.EventKey = "pr/1/" + strings.Repeat("a", 40) + "/" + strings.Repeat("c", 40)
	request.BaseOID = strings.Repeat("c", 40)
	request.PullRequestNumber = 1
	request.TriggerRef = "main"
	return request
}

// admit is the fixture shortcut for one accepted admission.
func (fixture *checkJobFixture) admit(t *testing.T, request CheckJobRequest) CheckJob {
	t.Helper()
	job, deduped, err := fixture.store.AdmitCheckJob(context.Background(), request, fixture.now)
	if err != nil || deduped {
		t.Fatalf("admit deduped=%v err=%v", deduped, err)
	}
	return job
}

func (fixture *checkJobFixture) registerJobAttempt(t *testing.T, job CheckJob, credential RunnerCredential) CheckAttempt {
	t.Helper()
	ctx := context.Background()
	attempt := CheckAttempt{
		ID: attemptID(job.ID, fixture.now), TaskID: job.TaskID, RepositoryID: job.RepositoryID,
		RevisionOID: job.SourceOID, WorktreeState: WorktreeClean, StartedAt: fixture.now, CreatedAt: fixture.now,
		JobID: job.ID, CredentialID: credential.ID,
		Checks: []CheckDefinition{{Name: "unit", Command: "go test ./..."}},
	}
	_, stored, err := fixture.store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatalf("register job attempt: %v", err)
	}
	return stored
}

func completeJobAttempt(t *testing.T, store *Store, attempt CheckAttempt, status string, now time.Time) (Task, CheckAttempt) {
	t.Helper()
	exit := 0
	if status != AttemptPassed {
		exit = 1
	}
	completion := CheckCompletion{
		AttemptID: attempt.ID, RepositoryID: attempt.RepositoryID, TaskID: attempt.TaskID,
		Results: []CheckResult{{
			Name: "unit", Command: "go test ./...", Status: status, ExitCode: &exit, DurationMS: 10, OutputExcerpt: "out",
		}},
		FinishedAt: now, WorktreeState: WorktreeClean, Log: "log",
	}
	job, found, err := store.CheckJob(context.Background(), attempt.RepositoryID, attempt.JobID)
	if err != nil || !found {
		t.Fatalf("read job completion authority: found=%v err=%v", found, err)
	}
	authority := CheckJobCompletionAuthority{
		JobID: job.ID, LeaseID: job.LeaseID, CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration,
	}
	task, stored, err := store.CompleteCheckJobAttempt(context.Background(), completion, authority, now)
	if err != nil {
		t.Fatalf("complete job attempt: %v", err)
	}
	return task, stored
}

func TestCheckPolicyChangeInvalidatesConsent(t *testing.T) {
	fixture := newCheckJobFixture(t)
	policy := fixture.setPolicy(t, nil)
	if policy.Version != 1 || policy.Digest == "" || policy.RunnerGeneration != 0 {
		t.Fatalf("created policy=%+v", policy)
	}
	granted := fixture.grantConsent(t)
	if !granted.ConsentActive || granted.ConsentVersion != 1 || granted.ConsentDigest != granted.Digest {
		t.Fatalf("granted consent=%+v", granted)
	}
	if _, deduped, err := fixture.store.AdmitCheckJob(context.Background(), pushJobRequest(), fixture.now); err != nil || deduped {
		t.Fatalf("admission with consent deduped=%v err=%v", deduped, err)
	}
	changed := fixture.setPolicy(t, func(input *CheckPolicyInput) { input.QueueLimit = 8 })
	if changed.Version != 2 || changed.Digest == policy.Digest || changed.ConsentActive || changed.ConsentVersion != 1 {
		t.Fatalf("changed policy=%+v", changed)
	}
	if _, _, err := fixture.store.AdmitCheckJob(context.Background(), pushJobRequest(), fixture.now); !errors.Is(err, ErrCheckConsentRequired) {
		t.Fatalf("admission after policy change error=%v", err)
	}
	regranted := fixture.grantConsent(t)
	if !regranted.ConsentActive || regranted.ConsentVersion != 2 {
		t.Fatalf("regranted consent=%+v", regranted)
	}
	if _, _, err := fixture.store.AdmitCheckJob(context.Background(), pushJobRequest(), fixture.now); err != nil {
		t.Fatalf("admission after regrant: %v", err)
	}
	same := fixture.setPolicy(t, func(input *CheckPolicyInput) { input.QueueLimit = 8 })
	if same.Version != changed.Version {
		t.Fatalf("idempotent policy set version=%d", same.Version)
	}
}

func TestCheckPolicyValidation(t *testing.T) {
	fixture := newCheckJobFixture(t)
	for name, mutate := range map[string]func(*CheckPolicyInput){
		"empty events":     func(input *CheckPolicyInput) { input.AllowedEvents = nil },
		"unknown event":    func(input *CheckPolicyInput) { input.AllowedEvents = []string{"tag"} },
		"repeated event":   func(input *CheckPolicyInput) { input.AllowedEvents = []string{"push", "push"} },
		"unknown executor": func(input *CheckPolicyInput) { input.Executor = "vm" },
		"small timeout":    func(input *CheckPolicyInput) { input.MaxTimeoutMS = 10 },
		"large output":     func(input *CheckPolicyInput) { input.MaxOutputLimitBytes = 1 << 30 },
		"zero queue":       func(input *CheckPolicyInput) { input.QueueLimit = 0 },
		"zero active":      func(input *CheckPolicyInput) { input.MaxActiveJobs = 0 },
		"small lease":      func(input *CheckPolicyInput) { input.MaxLeaseMS = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			input := defaultPolicyInput()
			input.RepositoryID = "other"
			mutate(&input)
			if _, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now); !errors.Is(err, ErrInvalidCheckPolicy) {
				t.Fatalf("policy error=%v", err)
			}
		})
	}
	if _, _, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := fixture.store.CheckPolicy(context.Background(), "project"); err != nil || exists {
		t.Fatalf("unexpected policy exists=%v err=%v", exists, err)
	}
}

func TestImmutableContainerImageAcceptsIDsAndDigestsButRejectsTags(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, image := range []string{"sha256:" + digest, "example.invalid/checks@sha256:" + digest} {
		if !validImmutableContainerImage(image) {
			t.Fatalf("immutable image %q was rejected", image)
		}
	}
	for _, image := range []string{"checks:latest", "checks@sha256:short", "sha256:" + strings.Repeat("A", 64)} {
		if validImmutableContainerImage(image) {
			t.Fatalf("mutable or malformed image %q was accepted", image)
		}
	}
}

func TestContainerPolicyRequiresImmutableImageAndCapturesRestrictions(t *testing.T) {
	fixture := newCheckJobFixture(t)
	input := defaultPolicyInput()
	input.Executor = CheckExecutorContainer
	if _, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now); !errors.Is(err, ErrInvalidCheckPolicy) {
		t.Fatalf("mutable or missing image error=%v", err)
	}
	input.Execution.ContainerImage = "sha256:" + strings.Repeat("a", 64)
	policy, err := fixture.store.SetCheckPolicy(context.Background(), input, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Execution.ContainerRuntime != "docker-local" || policy.Execution.ContainerNetwork != ContainerNetworkNone ||
		policy.Execution.ContainerCPUMillis == 0 || policy.Execution.ContainerMemoryBytes == 0 ||
		policy.Execution.ContainerPIDs == 0 || policy.Execution.ContainerScratchBytes == 0 {
		t.Fatalf("container defaults were not captured: %+v", policy.Execution)
	}
}

func TestContainerRuntimeOwnershipUsesExactJobContainerAndDaemon(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, func(input *CheckPolicyInput) {
		input.Executor = CheckExecutorContainer
		input.Execution.ContainerImage = "example.invalid/checks@sha256:" + strings.Repeat("a", 64)
	})
	fixture.grantConsent(t)
	job := fixture.admit(t, pushJobRequest())
	claimed, found, err := fixture.store.ClaimLocalCheckJob(context.Background(), "project", fixture.now)
	if err != nil || !found || claimed.ID != job.ID {
		t.Fatalf("local container claim=%+v found=%v err=%v", claimed, found, err)
	}
	authority := CheckJobCompletionAuthority{JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: claimed.CredentialID, CredentialGeneration: claimed.CredentialGeneration}
	containerID := strings.Repeat("b", 64)
	if err := fixture.store.PlanCheckContainer(context.Background(), authority, "owngit-check-test", "daemon-one", fixture.now); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.ConfirmCheckContainer(context.Background(), job.ID, "owngit-check-test", containerID, "daemon-one"); err != nil {
		t.Fatal(err)
	}
	ownership, err := fixture.store.ActiveCheckContainers(context.Background(), 10)
	if err != nil || len(ownership) != 1 || ownership[0].ContainerName != "owngit-check-test" || ownership[0].ContainerID != containerID || ownership[0].DaemonID != "daemon-one" {
		t.Fatalf("runtime ownership=%+v err=%v", ownership, err)
	}
	if err := fixture.store.ClearCheckContainer(context.Background(), job.ID, containerID, "other-daemon"); !errors.Is(err, ErrCheckJobRuntimeOwned) {
		t.Fatalf("foreign daemon clear error=%v", err)
	}
	if err := fixture.store.ClearCheckContainer(context.Background(), job.ID, containerID, "daemon-one"); err != nil {
		t.Fatal(err)
	}
}

func TestAdmitCheckJobDeduplicatesAndTightensLimits(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, func(input *CheckPolicyInput) {
		input.MaxTimeoutMS = 5000
		input.MaxOutputLimitBytes = 4096
	})
	fixture.grantConsent(t)
	request := pushJobRequest()
	request.TimeoutMS = 600000
	request.OutputLimitBytes = 1024
	job := fixture.admit(t, request)
	if job.Limits.TimeoutMS != 5000 || job.Limits.OutputLimitBytes != 1024 {
		t.Fatalf("effective limits=%+v", job.Limits)
	}
	if job.Executor != CheckExecutorExternalRunner || job.PolicyVersion != 1 || job.ConsentVersion != 1 || job.Status != CheckJobPending {
		t.Fatalf("admitted job=%+v", job)
	}
	again, deduped, err := fixture.store.AdmitCheckJob(context.Background(), request, fixture.now.Add(time.Minute))
	if err != nil || !deduped || again.ID != job.ID {
		t.Fatalf("dedup id=%q deduped=%v err=%v", again.ID, deduped, err)
	}
	if _, err := fixture.store.GrantCheckConsent(context.Background(), "project", fixture.now); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionRequiresAllowedEventAndOwnsStableTask(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, func(input *CheckPolicyInput) { input.AllowedEvents = []string{checkworkflow.EventPush} })
	fixture.grantConsent(t)
	if _, _, err := fixture.store.AdmitCheckJob(context.Background(), pullRequestJobRequest(), fixture.now); !errors.Is(err, ErrCheckEventNotAllowed) {
		t.Fatalf("disallowed event error=%v", err)
	}
	first := fixture.admit(t, pushJobRequest())
	if !validAttemptID(first.TaskID) {
		t.Fatalf("job task identity=%q", first.TaskID)
	}
	if _, err := fixture.store.CancelCheckJob(context.Background(), "project", first.ID, fixture.now); err != nil {
		t.Fatal(err)
	}
	changed := pushJobRequest()
	changed.EventKey = "refs/heads/main@" + strings.Repeat("d", 40)
	changed.SourceOID = strings.Repeat("d", 40)
	second := fixture.admit(t, changed)
	if second.ID == first.ID || second.TaskID != first.TaskID {
		t.Fatalf("stable task first=%+v second=%+v", first, second)
	}
}

func TestPolicyChangeInterruptsStaleWorkWithoutStrandingEligibleJob(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	stale := fixture.admit(t, pushJobRequest())
	fixture.setPolicy(t, func(input *CheckPolicyInput) { input.QueueLimit++ })
	fixture.grantConsent(t)
	freshRequest := pushJobRequest()
	freshRequest.EventKey = "refs/heads/main@" + strings.Repeat("d", 40)
	freshRequest.SourceOID = strings.Repeat("d", 40)
	fresh := fixture.admit(t, freshRequest)
	runner, _ := fixture.issueRunner(t)
	claimed, found, err := fixture.store.ClaimCheckJob(context.Background(), "project", runner.ID, fixture.now.Add(time.Second))
	if err != nil || !found || claimed.ID != fresh.ID || claimed.Executor != CheckExecutorExternalRunner {
		t.Fatalf("eligible claim=%+v found=%v err=%v", claimed, found, err)
	}
	invalidated, found, err := fixture.store.CheckJob(context.Background(), "project", stale.ID)
	if err != nil || !found || invalidated.Status != CheckJobInterrupted || invalidated.FinishedAt == nil {
		t.Fatalf("stale job=%+v found=%v err=%v", invalidated, found, err)
	}
}

func TestLocalAndExternalClaimAuthoritiesAreSeparated(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, func(input *CheckPolicyInput) { input.Executor = CheckExecutorHost })
	fixture.grantConsent(t)
	job := fixture.admit(t, pushJobRequest())
	runner, _ := fixture.issueRunner(t)
	if _, claimed, err := fixture.store.ClaimCheckJob(context.Background(), "project", runner.ID, fixture.now); err != nil || claimed {
		t.Fatalf("external credential claimed host job: claimed=%v err=%v", claimed, err)
	}
	claimed, found, err := fixture.store.ClaimLocalCheckJob(context.Background(), "project", fixture.now)
	if err != nil || !found || claimed.ID != job.ID || claimed.CredentialRole != RunnerRoleServer {
		t.Fatalf("local claim=%+v found=%v err=%v", claimed, found, err)
	}
}

func TestRevokedConsentCannotStartClaimedJob(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	job := fixture.admit(t, pushJobRequest())
	runner, _ := fixture.issueRunner(t)
	claimed, found, err := fixture.store.ClaimCheckJob(context.Background(), "project", runner.ID, fixture.now)
	if err != nil || !found {
		t.Fatalf("claim found=%v err=%v", found, err)
	}
	if _, err := fixture.store.RevokeCheckConsent(context.Background(), "project", fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.StartCheckJob(context.Background(), CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now.Add(2*time.Second)); err == nil {
		t.Fatal("revoked consent authorized a new start")
	}
	stored, _, err := fixture.store.CheckJob(context.Background(), "project", job.ID)
	if err != nil || stored.Status != CheckJobInterrupted || stored.StartedAt != nil {
		t.Fatalf("revoked consent job=%+v err=%v", stored, err)
	}
}

func TestAdmitCheckJobEnforcesQueueAndConcurrency(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, func(input *CheckPolicyInput) { input.QueueLimit = 3 })
	fixture.grantConsent(t)
	var admitted sync.Map
	var failures int
	var lock sync.Mutex
	var wait sync.WaitGroup
	for index := 0; index < 12; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			request := pushJobRequest()
			request.EventKey = fmt.Sprintf("concurrent-%d", index)
			request.SourceOID = fmt.Sprintf("%040d", index)
			_, _, err := fixture.store.AdmitCheckJob(context.Background(), request, fixture.now)
			if errors.Is(err, ErrCheckQueueFull) {
				lock.Lock()
				failures++
				lock.Unlock()
				return
			}
			if err != nil {
				t.Errorf("concurrent admission: %v", err)
				return
			}
			admitted.Store(index, true)
		}(index)
	}
	wait.Wait()
	successes := 0
	admitted.Range(func(_, _ any) bool { successes++; return true })
	jobs, err := fixture.store.CheckJobs(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if successes != 3 || failures != 9 || len(jobs) != 3 {
		t.Fatalf("jobs=%d successes=%d failures=%d", len(jobs), successes, failures)
	}
	overflow := pushJobRequest()
	overflow.EventKey = "push-overflow"
	overflow.SourceOID = strings.Repeat("f", 40)
	if _, _, err := fixture.store.AdmitCheckJob(context.Background(), overflow, fixture.now); !errors.Is(err, ErrCheckQueueFull) {
		t.Fatalf("overflow error=%v", err)
	}
}

func TestConcurrentAdmissionDeduplicatesOneJob(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	request := pushJobRequest()
	ids := make([]string, 0, 8)
	var lock sync.Mutex
	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			job, _, err := fixture.store.AdmitCheckJob(context.Background(), request, fixture.now)
			if err != nil {
				t.Errorf("concurrent dedup: %v", err)
				return
			}
			lock.Lock()
			ids = append(ids, job.ID)
			lock.Unlock()
		}()
	}
	wait.Wait()
	jobs, err := fixture.store.CheckJobs(context.Background(), "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("dedup created %d jobs", len(jobs))
	}
	for _, id := range ids {
		if id != jobs[0].ID {
			t.Fatalf("dedup returned foreign job %q", id)
		}
	}
}

func TestJobClaimStartCompleteLifecycle(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	job := fixture.admit(t, pushJobRequest())
	runner, _ := fixture.issueRunner(t)
	ctx := context.Background()

	if _, claimed, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now); err != nil || !claimed {
		t.Fatalf("first claim claimed=%v err=%v", claimed, err)
	}
	second, _ := fixture.issueRunner(t)
	if _, claimed, err := fixture.store.ClaimCheckJob(ctx, "project", second.ID, fixture.now); err != nil || claimed {
		t.Fatalf("second claim claimed=%v err=%v", claimed, err)
	}
	claimed, exists, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || !exists || claimed.Status != CheckJobClaimed || claimed.LeaseID == "" || claimed.LeaseExpiresAt == nil {
		t.Fatalf("claimed job=%+v exists=%v err=%v", claimed, exists, err)
	}
	if claimed.CredentialID != runner.ID || claimed.CredentialGeneration != runner.Generation || claimed.CredentialRole != RunnerRoleExternal {
		t.Fatalf("claim credential=%s generation=%d role=%s", claimed.CredentialID, claimed.CredentialGeneration, claimed.CredentialRole)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: strings.Repeat("0", 32),
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now); !errors.Is(err, ErrCheckJobLease) {
		t.Fatalf("wrong lease error=%v", err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now); err != nil {
		t.Fatalf("start job: %v", err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now.Add(time.Second)); !errors.Is(err, ErrCheckJobStartReplay) {
		t.Fatalf("duplicate start error=%v", err)
	}
	attempt := fixture.registerJobAttempt(t, claimed, runner)
	if attempt.JobID != job.ID || attempt.ExecutionScope != ExecutionScopeExternalRunner || attempt.Protection != ProtectionUnknown {
		t.Fatalf("derived attempt=%+v", attempt)
	}
	completeJobAttempt(t, fixture.store, attempt, AttemptPassed, fixture.now.Add(2*time.Second))
	if _, replayedAttempt, err := fixture.store.RegisterCheckAttempt(ctx, attempt); err != nil || replayedAttempt.Sequence != attempt.Sequence {
		t.Fatalf("terminal registration replay=%+v err=%v", replayedAttempt, err)
	}
	stored, exists, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || !exists || stored.Status != CheckJobPassed || stored.FinishedAt == nil || stored.Summary == "" {
		t.Fatalf("terminal job=%+v exists=%v err=%v", stored, exists, err)
	}
	// The attempt completion replay never advances the job twice.
	completeJobAttempt(t, fixture.store, attempt, AttemptPassed, fixture.now.Add(2*time.Second))
	replayed, _, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || replayed.Status != CheckJobPassed {
		t.Fatalf("replay job=%+v err=%v", replayed, err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now); !errors.Is(err, ErrCheckJobState) {
		t.Fatalf("start terminal job error=%v", err)
	}
}

func TestJobOriginCannotBeForgedByHelperFacts(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, func(input *CheckPolicyInput) { input.Executor = CheckExecutorExternalRunner })
	fixture.grantConsent(t)
	job := fixture.admit(t, pushJobRequest())
	runner, _ := fixture.issueRunner(t)
	ctx := context.Background()
	claimed, _, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation, Protection: ProtectionRunnerReported,
	}, fixture.now); err != nil {
		t.Fatal(err)
	}
	attempt := CheckAttempt{
		ID: attemptID("spoof", fixture.now), TaskID: job.TaskID, RepositoryID: "project",
		RevisionOID: job.SourceOID, WorktreeState: WorktreeClean, StartedAt: fixture.now, CreatedAt: fixture.now,
		JobID: job.ID, CredentialID: runner.ID,
		// A helper-style payload that claims inherited/unknown is overwritten.
		ExecutionScope: ExecutionScopeInherited, Protection: ProtectionUnknown,
		Checks: []CheckDefinition{{Name: "unit", Command: "go test ./..."}},
	}
	// A foreign credential cannot bind an otherwise unbound started job.
	foreign := attempt
	foreign.ID = attemptID("foreign", fixture.now)
	foreign.CredentialID = strings.Repeat("0", 32)
	if _, _, err := fixture.store.RegisterCheckAttempt(ctx, foreign); !errors.Is(err, ErrCheckJobCredential) {
		t.Fatalf("foreign credential error=%v", err)
	}
	_, stored, err := fixture.store.RegisterCheckAttempt(ctx, attempt)
	if err != nil {
		t.Fatalf("register spoofed attempt: %v", err)
	}
	if stored.ExecutionScope != ExecutionScopeExternalRunner || stored.Protection != ProtectionRunnerReported || stored.CredentialID != runner.ID {
		t.Fatalf("derived origin=%+v", stored)
	}
	if _, replayed, err := fixture.store.RegisterCheckAttempt(ctx, attempt); err != nil || replayed.Sequence != stored.Sequence {
		t.Fatalf("replay sequence=%d err=%v", replayed.Sequence, err)
	}
	otherTask, err := fixture.store.CreateTask(ctx, "project", "Other", fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	// A second different attempt cannot bind the same job.
	bound := attempt
	bound.ID = attemptID("bound", fixture.now)
	bound.TaskID = otherTask.ID
	if _, _, err := fixture.store.RegisterCheckAttempt(ctx, bound); !errors.Is(err, ErrCheckJobAttemptBound) {
		t.Fatalf("second attempt error=%v", err)
	}
	// An unknown job and a foreign-repository job are rejected.
	unknown := attempt
	unknown.ID = attemptID("unknown", fixture.now)
	unknown.TaskID = otherTask.ID
	unknown.JobID = strings.Repeat("9", 32)
	if _, _, err := fixture.store.RegisterCheckAttempt(ctx, unknown); !errors.Is(err, ErrCheckJobNotFound) {
		t.Fatalf("unknown job error=%v", err)
	}
}

func TestJobAttemptFactsAndCompletionAuthorityAreExact(t *testing.T) {
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
	otherTask, err := fixture.store.CreateTask(ctx, "project", "Other", fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	base := CheckAttempt{
		ID: attemptID("exact", fixture.now), TaskID: job.TaskID, RepositoryID: "project",
		RevisionOID: job.SourceOID, WorktreeState: WorktreeClean, StartedAt: fixture.now, CreatedAt: fixture.now,
		JobID: job.ID, CredentialID: runner.ID,
		Checks: []CheckDefinition{{Name: "unit", Command: "go test ./..."}},
	}
	for _, test := range []struct {
		name   string
		mutate func(*CheckAttempt)
	}{
		{name: "revision", mutate: func(attempt *CheckAttempt) { attempt.RevisionOID = strings.Repeat("c", 40) }},
		{name: "commands", mutate: func(attempt *CheckAttempt) { attempt.Checks[0].Command = "true" }},
		{name: "limits", mutate: func(attempt *CheckAttempt) { attempt.TimeoutMS = job.Limits.TimeoutMS + 1 }},
		{name: "task", mutate: func(attempt *CheckAttempt) { attempt.TaskID = otherTask.ID }},
		{name: "correction cycle", mutate: func(attempt *CheckAttempt) { attempt.CycleID = strings.Repeat("c", 32) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Checks = append([]CheckDefinition(nil), base.Checks...)
			candidate.ID = attemptID(test.name, fixture.now)
			test.mutate(&candidate)
			if _, _, err := fixture.store.RegisterCheckAttempt(ctx, candidate); !errors.Is(err, ErrCheckJobFacts) {
				t.Fatalf("mismatched registration error=%v", err)
			}
		})
	}
	_, attempt, err := fixture.store.RegisterCheckAttempt(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	exit := 0
	completion := CheckCompletion{
		AttemptID: attempt.ID, RepositoryID: attempt.RepositoryID, TaskID: attempt.TaskID,
		Results:    []CheckResult{{Name: "unit", Command: "go test ./...", Status: AttemptPassed, ExitCode: &exit, DurationMS: 1}},
		FinishedAt: fixture.now.Add(time.Second), WorktreeState: WorktreeClean,
	}
	if _, _, err := fixture.store.CompleteCheckAttempt(ctx, completion, fixture.now.Add(time.Second)); !errors.Is(err, ErrCheckJobCompletionRequired) {
		t.Fatalf("ordinary completion error=%v", err)
	}
	wrong := CheckJobCompletionAuthority{
		JobID: job.ID, LeaseID: strings.Repeat("0", 32), CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}
	if _, _, err := fixture.store.CompleteCheckJobAttempt(ctx, completion, wrong, fixture.now.Add(time.Second)); !errors.Is(err, ErrCheckJobLease) {
		t.Fatalf("wrong completion authority error=%v", err)
	}
	correct := CheckJobCompletionAuthority{
		JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}
	if _, stored, err := fixture.store.CompleteCheckJobAttempt(ctx, completion, correct, fixture.now.Add(time.Second)); err != nil || stored.Status != AttemptPassed {
		t.Fatalf("authorized completion=%+v err=%v", stored, err)
	}
}

func TestStartReplayNeverGrantsExecution(t *testing.T) {
	tests := []struct {
		name       string
		invalidate func(*testing.T, *checkJobFixture, RunnerCredential) time.Time
		wantStatus string
	}{
		{
			name: "consent revoked",
			invalidate: func(t *testing.T, fixture *checkJobFixture, _ RunnerCredential) time.Time {
				if _, err := fixture.store.RevokeCheckConsent(context.Background(), "project", fixture.now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				return fixture.now.Add(2 * time.Second)
			},
			wantStatus: CheckJobStarted,
		},
		{
			name: "credential revoked",
			invalidate: func(t *testing.T, fixture *checkJobFixture, runner RunnerCredential) time.Time {
				if err := fixture.store.RevokeCheckRunnerToken(context.Background(), "project", runner.ID, fixture.now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				return fixture.now.Add(2 * time.Second)
			},
			wantStatus: CheckJobStarted,
		},
		{
			name: "policy changed",
			invalidate: func(t *testing.T, fixture *checkJobFixture, _ RunnerCredential) time.Time {
				fixture.setPolicy(t, func(input *CheckPolicyInput) {
					input.Executor = CheckExecutorExternalRunner
					input.QueueLimit++
				})
				return fixture.now.Add(2 * time.Second)
			},
			wantStatus: CheckJobStarted,
		},
		{
			name: "lease expired",
			invalidate: func(t *testing.T, fixture *checkJobFixture, _ RunnerCredential) time.Time {
				if expired, err := fixture.store.ExpireCheckJobLeases(context.Background(), fixture.now.Add(2*time.Minute)); err != nil || expired != 1 {
					t.Fatalf("expired=%d err=%v", expired, err)
				}
				return fixture.now.Add(3 * time.Minute)
			},
			wantStatus: CheckJobAmbiguous,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCheckJobFixture(t)
			fixture.setPolicy(t, func(input *CheckPolicyInput) { input.Executor = CheckExecutorExternalRunner })
			fixture.grantConsent(t)
			job := fixture.admit(t, pushJobRequest())
			runner, _ := fixture.issueRunner(t)
			ctx := context.Background()
			claimed, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
			if err != nil || !found {
				t.Fatalf("claim found=%v err=%v", found, err)
			}
			request := CheckJobStart{
				RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
				CredentialID: runner.ID, CredentialGeneration: runner.Generation, Protection: ProtectionRunnerReported,
			}
			first, err := fixture.store.StartCheckJob(ctx, request, fixture.now)
			if err != nil {
				t.Fatal(err)
			}
			replayAt := test.invalidate(t, fixture, runner)
			if _, err := fixture.store.StartCheckJob(ctx, request, replayAt); !errors.Is(err, ErrCheckJobStartReplay) {
				t.Fatalf("start replay error=%v", err)
			}
			stored, found, err := fixture.store.CheckJob(ctx, "project", job.ID)
			if err != nil || !found || stored.Status != test.wantStatus || stored.StartedAt == nil ||
				!stored.StartedAt.Equal(*first.StartedAt) || stored.Protection != first.Protection {
				t.Fatalf("stored start facts=%+v found=%v err=%v", stored, found, err)
			}
		})
	}
}

func TestLeaseExpiryIsAmbiguousAndNeedsExplicitRerun(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	request := pushJobRequest()
	job := fixture.admit(t, request)
	runner, _ := fixture.issueRunner(t)
	ctx := context.Background()
	claimed, _, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now); err != nil {
		t.Fatal(err)
	}
	expiredAt := fixture.now.Add(2 * time.Minute)
	expired, err := fixture.store.ExpireCheckJobLeases(ctx, expiredAt)
	if err != nil || expired != 1 {
		t.Fatalf("expire count=%d err=%v", expired, err)
	}
	stored, _, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || stored.Status != CheckJobAmbiguous || stored.LeaseLostAt == nil {
		t.Fatalf("ambiguous job=%+v err=%v", stored, err)
	}
	// Re-observing the same event dedups onto the ambiguous job, so no silent
	// requeue happens.
	again, deduped, err := fixture.store.AdmitCheckJob(ctx, request, expiredAt)
	if err != nil || !deduped || again.ID != job.ID || again.Status != CheckJobAmbiguous {
		t.Fatalf("dedup after loss id=%q deduped=%v status=%s err=%v", again.ID, deduped, again.Status, err)
	}
	if _, claimed, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, expiredAt); err != nil || claimed {
		t.Fatalf("ambiguous claim claimed=%v err=%v", claimed, err)
	}
	rerun, deduped, err := fixture.store.RerunCheckJob(ctx, "project", job.ID, expiredAt)
	if err != nil || deduped || rerun.ID == job.ID || rerun.Status != CheckJobPending || rerun.RerunRoot != job.ID || rerun.RerunGeneration != 1 {
		t.Fatalf("rerun=%+v deduped=%v err=%v", rerun, deduped, err)
	}
	if _, claimed, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, expiredAt); err != nil || !claimed {
		t.Fatalf("rerun claim claimed=%v err=%v", claimed, err)
	}
	if _, _, err := fixture.store.RerunCheckJob(ctx, "project", rerun.ID, expiredAt); !errors.Is(err, ErrCheckJobState) {
		t.Fatalf("rerun pending error=%v", err)
	}
}

func TestLateCompletionAfterLeaseLossRecordsVerdict(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	job := fixture.admit(t, pushJobRequest())
	runner, _ := fixture.issueRunner(t)
	ctx := context.Background()
	claimed, _, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now); err != nil {
		t.Fatal(err)
	}
	attempt := fixture.registerJobAttempt(t, claimed, runner)
	if _, err := fixture.store.ExpireCheckJobLeases(ctx, fixture.now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	completeJobAttempt(t, fixture.store, attempt, AttemptFailed, fixture.now.Add(3*time.Minute))
	stored, _, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || stored.Status != CheckJobFailed || stored.LeaseLostAt == nil || stored.FinishedAt == nil {
		t.Fatalf("late completion job=%+v err=%v", stored, err)
	}
}

func TestRevokedCredentialCannotCompleteStartedJob(t *testing.T) {
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
	if err := fixture.store.RevokeCheckRunnerToken(ctx, "project", runner.ID, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	exit := 0
	completion := CheckCompletion{
		AttemptID: attempt.ID, RepositoryID: attempt.RepositoryID, TaskID: attempt.TaskID,
		Results:    []CheckResult{{Name: "unit", Command: "go test ./...", Status: AttemptPassed, ExitCode: &exit, DurationMS: 1}},
		FinishedAt: fixture.now.Add(2 * time.Second), WorktreeState: WorktreeClean,
	}
	authority := CheckJobCompletionAuthority{
		JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}
	if _, _, err := fixture.store.CompleteCheckJobAttempt(ctx, completion, authority, fixture.now.Add(2*time.Second)); !errors.Is(err, ErrCheckRunnerCredential) {
		t.Fatalf("revoked completion error=%v", err)
	}
	stored, _, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || stored.Status != CheckJobStarted {
		t.Fatalf("revoked completion changed job=%+v err=%v", stored, err)
	}
}

func TestClaimedJobCancellationPreventsStart(t *testing.T) {
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
	cancelled, err := fixture.store.CancelCheckJob(ctx, "project", job.ID, fixture.now.Add(time.Second))
	if err != nil || cancelled.Status != CheckJobCancelled || cancelled.ClaimedAt == nil || cancelled.StartedAt != nil ||
		cancelled.FinishedAt == nil || cancelled.CancelRequestedAt == nil || cancelled.LeaseID != claimed.LeaseID ||
		cancelled.CredentialID != runner.ID || cancelled.AttemptID != "" {
		t.Fatalf("cancelled claimed job=%+v err=%v", cancelled, err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now.Add(2*time.Second)); !errors.Is(err, ErrCheckJobState) {
		t.Fatalf("start after cancellation error=%v", err)
	}
	stored, found, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || !found || stored.Status != CheckJobCancelled || stored.StartedAt != nil || stored.AttemptID != "" {
		t.Fatalf("stored cancelled claim=%+v found=%v err=%v", stored, found, err)
	}
	if _, err := fixture.store.RecoverySnapshot(ctx); err != nil {
		t.Fatalf("cancelled claim recovery: %v", err)
	}
}

func TestClaimCancellationRaceDoesNotMisreportStoppedExecution(t *testing.T) {
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
	request := CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}
	begin := make(chan struct{})
	var wait sync.WaitGroup
	var started, cancelled CheckJob
	var startErr, cancelErr error
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-begin
		started, startErr = fixture.store.StartCheckJob(ctx, request, fixture.now.Add(time.Second))
	}()
	go func() {
		defer wait.Done()
		<-begin
		cancelled, cancelErr = fixture.store.CancelCheckJob(ctx, "project", job.ID, fixture.now.Add(time.Second))
	}()
	close(begin)
	wait.Wait()
	if cancelErr != nil {
		t.Fatalf("cancel race error=%v", cancelErr)
	}
	stored, found, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || !found {
		t.Fatalf("read raced job found=%v err=%v", found, err)
	}
	switch stored.Status {
	case CheckJobCancelled:
		if !errors.Is(startErr, ErrCheckJobState) || cancelled.Status != CheckJobCancelled || stored.StartedAt != nil || stored.FinishedAt == nil {
			t.Fatalf("cancel won start=%+v startErr=%v cancel=%+v stored=%+v", started, startErr, cancelled, stored)
		}
	case CheckJobStarted:
		if startErr != nil || started.Status != CheckJobStarted || cancelled.Status != CheckJobStarted || stored.StartedAt == nil || stored.CancelRequestedAt == nil || stored.FinishedAt != nil {
			t.Fatalf("start won start=%+v startErr=%v cancel=%+v stored=%+v", started, startErr, cancelled, stored)
		}
	default:
		t.Fatalf("unexpected raced job=%+v startErr=%v cancel=%+v", stored, startErr, cancelled)
	}
}

func TestJobCancellationSemantics(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	ctx := context.Background()
	pending := fixture.admit(t, pushJobRequest())
	cancelled, err := fixture.store.CancelCheckJob(ctx, "project", pending.ID, fixture.now)
	if err != nil || cancelled.Status != CheckJobCancelled || cancelled.FinishedAt == nil || cancelled.CancelRequestedAt == nil {
		t.Fatalf("cancelled pending=%+v err=%v", cancelled, err)
	}
	if _, claimed, err := fixture.store.ClaimCheckJob(ctx, "project", mustRunner(t, fixture).ID, fixture.now); err != nil || claimed {
		t.Fatalf("claim cancelled claimed=%v err=%v", claimed, err)
	}

	request := pushJobRequest()
	request.EventKey = "refs/heads/main@" + strings.Repeat("d", 40)
	request.SourceOID = strings.Repeat("d", 40)
	job := fixture.admit(t, request)
	runner, _ := fixture.issueRunner(t)
	claimed, _, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CancelCheckJob(ctx, "project", job.ID, fixture.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	requested, _, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || requested.Status != CheckJobStarted || requested.CancelRequestedAt == nil {
		t.Fatalf("cancel request job=%+v err=%v", requested, err)
	}
	attempt := fixture.registerJobAttempt(t, claimed, runner)
	exit := 1
	completion := CheckCompletion{
		AttemptID: attempt.ID, RepositoryID: attempt.RepositoryID, TaskID: attempt.TaskID,
		Results:    []CheckResult{{Name: "unit", Command: "go test ./...", Status: AttemptCancelled, ExitCode: &exit, DurationMS: 1}},
		Cancelled:  true,
		FinishedAt: fixture.now.Add(2 * time.Second), WorktreeState: WorktreeClean,
	}
	authority := CheckJobCompletionAuthority{
		JobID: job.ID, LeaseID: claimed.LeaseID, CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}
	if _, _, err := fixture.store.CompleteCheckJobAttempt(ctx, completion, authority, fixture.now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	target, _, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || target.Status != CheckJobCancelled {
		t.Fatalf("cancelled job=%+v err=%v", target, err)
	}
}

func mustRunner(t *testing.T, fixture *checkJobFixture) RunnerCredential {
	t.Helper()
	runner, _ := fixture.issueRunner(t)
	return runner
}

func TestRunnerCredentialRevocationAndScope(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	ctx := context.Background()
	runner, token := fixture.issueRunner(t)
	if runner.Generation != 1 {
		t.Fatalf("first generation=%d", runner.Generation)
	}
	resolved, found, err := fixture.store.RunnerCredentialByToken(ctx, "project", token, fixture.now)
	if err != nil || !found || resolved.ID != runner.ID {
		t.Fatalf("resolve runner found=%v err=%v", found, err)
	}
	if _, found, err := fixture.store.RunnerCredentialByToken(ctx, "other", token, fixture.now); err != nil || found {
		t.Fatalf("cross-repository resolve found=%v err=%v", found, err)
	}
	job := fixture.admit(t, pushJobRequest())
	claimed, found, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now)
	if err != nil || !found {
		t.Fatalf("claim claimed=%v err=%v", found, err)
	}
	if err := fixture.store.RevokeCheckRunnerToken(ctx, "project", runner.ID, fixture.now); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.RevokeCheckRunnerToken(ctx, "project", runner.ID, fixture.now); !errors.Is(err, ErrCheckRunnerRevoked) {
		t.Fatalf("double revoke error=%v", err)
	}
	if _, found, err := fixture.store.RunnerCredentialByToken(ctx, "project", token, fixture.now); err != nil || found {
		t.Fatalf("revoked resolve found=%v err=%v", found, err)
	}
	if _, _, err := fixture.store.ClaimCheckJob(ctx, "project", runner.ID, fixture.now); !errors.Is(err, ErrCheckRunnerCredential) {
		t.Fatalf("revoked claim error=%v", err)
	}
	if _, err := fixture.store.StartCheckJob(ctx, CheckJobStart{
		RepositoryID: "project", JobID: job.ID, LeaseID: claimed.LeaseID,
		CredentialID: runner.ID, CredentialGeneration: runner.Generation,
	}, fixture.now); err == nil {
		t.Fatal("revoked authority started claimed work")
	}
	interrupted, _, err := fixture.store.CheckJob(ctx, "project", job.ID)
	if err != nil || interrupted.Status != CheckJobInterrupted {
		t.Fatalf("revoked job=%+v err=%v", interrupted, err)
	}
	// New issuance constructs a different epoch-bound bearer at a higher generation.
	next, nextToken, created, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "server", "", fixture.now)
	if err != nil || !created || next.Generation != runner.Generation+1 || nextToken == "" || nextToken == token {
		t.Fatalf("next generation=%d created=%v err=%v", next.Generation, created, err)
	}
	if _, found, err := fixture.store.RunnerCredentialByToken(ctx, "project", token, fixture.now); err != nil || found {
		t.Fatalf("retired bearer revived found=%v err=%v", found, err)
	}
	if _, found, err := fixture.store.RunnerCredentialByToken(ctx, "project", nextToken, fixture.now); err != nil || !found {
		t.Fatalf("new bearer found=%v err=%v", found, err)
	}
	// A job is scoped to its repository, so another repository cannot claim it.
	if _, _, err := fixture.store.ClaimCheckJob(ctx, "other", runner.ID, fixture.now); !errors.Is(err, ErrCheckRunnerCredential) {
		t.Fatalf("foreign repository claim error=%v", err)
	}
}

func TestRunnerCredentialCreationIsIdempotent(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	ctx := context.Background()
	creation := strings.Repeat("7", 32)
	first, token, created, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "laptop", creation, fixture.now)
	if err != nil || !created || token == "" {
		t.Fatalf("first created=%v err=%v", created, err)
	}
	again, replayToken, created, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "laptop", creation, fixture.now)
	if err != nil || created || again.ID != first.ID || replayToken != "" {
		t.Fatalf("retry created=%v id=%q token=%t err=%v", created, again.ID, replayToken != "", err)
	}
	if _, _, _, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "other", creation, fixture.now); !errors.Is(err, ErrCheckRunnerCreationConflict) {
		t.Fatalf("conflicting creation error=%v", err)
	}
	if err := fixture.store.RevokeCheckRunnerTokenByCreation(ctx, "project", creation, fixture.now); err != nil {
		t.Fatal(err)
	}
	retired, replayToken, created, err := fixture.store.IssueCheckRunnerToken(ctx, "project", "laptop", creation, fixture.now)
	if err != nil || created || replayToken != "" || retired.ID != first.ID || retired.RevokedAt == nil {
		t.Fatalf("revoked creation replay=%+v created=%v token=%t err=%v", retired, created, replayToken != "", err)
	}
	if _, found, err := fixture.store.RunnerCredentialByToken(ctx, "project", token, fixture.now); err != nil || found {
		t.Fatalf("compensated revoke found=%v err=%v", found, err)
	}
}

func TestObservationsAreBoundedAndUpserted(t *testing.T) {
	fixture := newCheckJobFixture(t)
	ctx := context.Background()
	for index := 0; index < MaximumCheckObservations+6; index++ {
		ref := fmt.Sprintf("refs/heads/branch-%02d", index)
		oid := fmt.Sprintf("%040d", index)
		if err := fixture.store.RecordCheckObservation(ctx, "project", ref, oid, fixture.now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	observations, err := fixture.store.CheckObservations(ctx, "project")
	if err != nil || len(observations) != MaximumCheckObservations {
		t.Fatalf("observation count=%d err=%v", len(observations), err)
	}
	if observations[0].RefName != fmt.Sprintf("refs/heads/branch-%02d", MaximumCheckObservations+5) {
		t.Fatalf("newest observation=%+v", observations[0])
	}
	updated := strings.Repeat("e", 40)
	if err := fixture.store.RecordCheckObservation(ctx, "project", "refs/heads/branch-63", updated, fixture.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	refreshed, err := fixture.store.CheckObservations(ctx, "project")
	if err != nil || refreshed[0].OID != updated || len(refreshed) != MaximumCheckObservations {
		t.Fatalf("upserted observation=%+v err=%v", refreshed[0], err)
	}
	for _, invalid := range []struct{ ref, oid string }{
		{"", strings.Repeat("a", 40)},
		{"main", strings.Repeat("a", 40)},
		{"refs/tags/newest", strings.Repeat("a", 40)},
		{"refs/owngit/private", strings.Repeat("a", 40)},
		{"refs/heads/@", strings.Repeat("a", 40)},
		{"refs/heads/main", "short"},
	} {
		if err := fixture.store.RecordCheckObservation(ctx, "project", invalid.ref, invalid.oid, fixture.now.Add(2*time.Hour)); !errors.Is(err, ErrInvalidCheckObservation) {
			t.Fatalf("invalid observation %q/%q error=%v", invalid.ref, invalid.oid, err)
		}
	}
	afterInvalid, err := fixture.store.CheckObservations(ctx, "project")
	if err != nil || len(afterInvalid) != MaximumCheckObservations || afterInvalid[0].RefName != "refs/heads/branch-63" {
		t.Fatalf("invalid refs changed branch observations=%+v err=%v", afterInvalid, err)
	}
}

func TestAdmitCheckJobRejectsMalformedRequests(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	ctx := context.Background()
	base := pushJobRequest()
	tests := map[string]func(*CheckJobRequest){
		"unknown trigger": func(request *CheckJobRequest) { request.Trigger = "merge" },
		"push with pr facts": func(request *CheckJobRequest) {
			request.PullRequestNumber = 1
			request.BaseOID = strings.Repeat("c", 40)
		},
		"pr without base":     func(request *CheckJobRequest) { request.Trigger = "pull_request"; request.PullRequestNumber = 1 },
		"bad source":          func(request *CheckJobRequest) { request.SourceOID = "short" },
		"empty event key":     func(request *CheckJobRequest) { request.EventKey = "" },
		"bad trigger ref":     func(request *CheckJobRequest) { request.TriggerRef = "a b" },
		"standalone at ref":   func(request *CheckJobRequest) { request.TriggerRef = "@" },
		"other workflow":      func(request *CheckJobRequest) { request.WorkflowPath = "ci.yml" },
		"bad workflow digest": func(request *CheckJobRequest) { request.WorkflowDigest = "x" },
		"no checks":           func(request *CheckJobRequest) { request.Checks = nil },
		"negative limits":     func(request *CheckJobRequest) { request.TimeoutMS = -1 },
		"bad rerun root":      func(request *CheckJobRequest) { request.RerunRoot = "abc" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, _, err := fixture.store.AdmitCheckJob(ctx, request, fixture.now); !errors.Is(err, ErrInvalidCheckJob) {
				t.Fatalf("request error=%v", err)
			}
		})
	}
	pr := pullRequestJobRequest()
	if _, deduped, err := fixture.store.AdmitCheckJob(ctx, pr, fixture.now); err != nil || deduped {
		t.Fatalf("pull request admission deduped=%v err=%v", deduped, err)
	}
}

func TestRerunAdmissionDeduplicatesUnderConcurrency(t *testing.T) {
	fixture := newCheckJobFixture(t)
	fixture.setPolicy(t, nil)
	fixture.grantConsent(t)
	job := fixture.admit(t, pushJobRequest())
	cancelled, err := fixture.store.CancelCheckJob(context.Background(), "project", job.ID, fixture.now)
	if err != nil || cancelled.Status != CheckJobCancelled {
		t.Fatalf("cancel err=%v", err)
	}
	ids := make([]string, 0, 4)
	var lock sync.Mutex
	var wait sync.WaitGroup
	for index := 0; index < 4; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			rerun, _, err := fixture.store.RerunCheckJob(context.Background(), "project", job.ID, fixture.now.Add(time.Second))
			if err != nil {
				t.Errorf("concurrent rerun: %v", err)
				return
			}
			lock.Lock()
			ids = append(ids, rerun.ID)
			lock.Unlock()
		}()
	}
	wait.Wait()
	if len(ids) != 4 {
		t.Fatalf("rerun results=%d", len(ids))
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("rerun dedup returned different jobs: %v", ids)
		}
	}
	jobs, err := fixture.store.CheckJobs(context.Background(), "project")
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs=%d err=%v", len(jobs), err)
	}
}
