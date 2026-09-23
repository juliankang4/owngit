// Package checkrun admits and executes repository-configured advisory checks.
package checkrun

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"owngit/internal/checkapi"
	"owngit/internal/checkexec"
	"owngit/internal/checksource"
	"owngit/internal/checkworkflow"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
)

const (
	RuntimeUnavailableWorkspace = "workspace_unavailable"
	RuntimeUnavailableRecovery  = "restart_reconciliation_unavailable"

	maximumObservedRefs     = 64
	maximumObservedPRs      = 64
	maximumAutomaticLogSize = 256 << 10
)

var ErrRuntimeUnavailable = errors.New("configured check runtime is unavailable")

// errRevisionRejected marks an admission failure that the revision itself
// determines, such as an unparsable or oversized workflow or a branch name the
// job record cannot carry. Retrying the same revision cannot succeed, so the
// reconciler records it as that revision's outcome and continues with the next
// one instead of blocking the rest of the repository.
var errRevisionRejected = errors.New("configured check workflow was rejected")

// RuntimeUnavailableError identifies a check-specific startup boundary that
// must disable automatic and external-runner authority without blocking Git.
type RuntimeUnavailableError struct {
	Code string
	Err  error
}

func (err *RuntimeUnavailableError) Error() string {
	return fmt.Sprintf("%s: %v", err.Code, err.Err)
}

func (err *RuntimeUnavailableError) Unwrap() error { return err.Err }

func (err *RuntimeUnavailableError) Is(target error) bool { return target == ErrRuntimeUnavailable }

// Coordinator owns bounded reconciliation and the in-process host/container
// worker. External-runner jobs are admitted here but claimed only over the
// separate runner protocol.
type Coordinator struct {
	Store         *state.Store
	Repositories  *repository.Manager
	PullRequests  *pullrequest.Service
	WorkspaceRoot string
	DockerPath    string
	Interval      time.Duration
	Logf          func(string, ...any)

	wake              chan struct{}
	cancel            context.CancelFunc
	done              chan struct{}
	workspace         *checksource.WorkspaceRoot
	pushCursor        map[string]string
	pullRequestCursor map[string]int64
	mu                sync.Mutex
}

func (coordinator *Coordinator) Start(parent context.Context) error {
	if coordinator == nil || coordinator.Store == nil || coordinator.Repositories == nil {
		return errors.New("configured check coordinator is unavailable")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.cancel != nil {
		return errors.New("configured check coordinator is already running")
	}
	if coordinator.PullRequests == nil {
		coordinator.PullRequests = &pullrequest.Service{Store: coordinator.Store, Repositories: coordinator.Repositories}
	}
	if coordinator.WorkspaceRoot == "" {
		coordinator.WorkspaceRoot = filepath.Join(coordinator.Store.Dir(), "runtime", "check-jobs")
	}
	if _, err := coordinator.Store.ReconcileCheckJobRestart(parent, time.Now().UTC()); err != nil {
		if parent.Err() != nil {
			return parent.Err()
		}
		return &RuntimeUnavailableError{Code: RuntimeUnavailableRecovery, Err: fmt.Errorf("reconcile configured checks after restart: %w", err)}
	}
	workspace, err := checksource.AcquireWorkspaceRoot(coordinator.WorkspaceRoot)
	if err != nil {
		return &RuntimeUnavailableError{Code: RuntimeUnavailableWorkspace, Err: fmt.Errorf("acquire check workspace root: %w", err)}
	}
	keepWorkspace := false
	defer func() {
		if !keepWorkspace {
			workspace.Close()
		}
	}()
	containerCleanupErr := coordinator.reconcileContainers(parent)
	if containerCleanupErr != nil {
		coordinator.log("configured check startup container cleanup: %v", containerCleanupErr)
	} else {
		removed, more, cleanupErr := workspace.Cleanup(1000)
		if cleanupErr != nil || more {
			coordinator.log("configured check startup workspace cleanup removed=%d more=%v error=%v", removed, more, cleanupErr)
		}
	}
	ctx, cancel := context.WithCancel(parent)
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	coordinator.cancel = cancel
	coordinator.wake = wake
	coordinator.done = done
	coordinator.workspace = workspace
	if coordinator.pushCursor == nil {
		coordinator.pushCursor = make(map[string]string)
	}
	if coordinator.pullRequestCursor == nil {
		coordinator.pullRequestCursor = make(map[string]int64)
	}
	keepWorkspace = true
	go coordinator.loop(ctx, wake, done)
	wake <- struct{}{}
	return nil
}

func (coordinator *Coordinator) Stop(ctx context.Context) error {
	coordinator.mu.Lock()
	cancel, done := coordinator.cancel, coordinator.done
	coordinator.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		coordinator.mu.Lock()
		var workspace *checksource.WorkspaceRoot
		if coordinator.done == done {
			coordinator.cancel = nil
			coordinator.wake = nil
			coordinator.done = nil
			workspace = coordinator.workspace
			coordinator.workspace = nil
		}
		coordinator.mu.Unlock()
		workspace.Close()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wake schedules a bounded reconciliation. repositoryID is accepted by write
// seams for future narrowing; reconciliation remains global so repeated wakes
// coalesce without an unbounded repository queue.
func (coordinator *Coordinator) Wake(repositoryID string) {
	coordinator.mu.Lock()
	wake := coordinator.wake
	coordinator.mu.Unlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (coordinator *Coordinator) loop(ctx context.Context, wake <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	interval := coordinator.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
		}
		if err := coordinator.reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
			coordinator.log("configured check reconciliation: %v", err)
		}
		if err := coordinator.runOneLocal(ctx); err != nil && !errors.Is(err, context.Canceled) {
			coordinator.log("configured check execution: %v", err)
		}
	}
}

func (coordinator *Coordinator) reconcile(ctx context.Context) error {
	if _, err := coordinator.Store.ExpireCheckJobLeases(ctx, time.Now().UTC()); err != nil {
		return err
	}
	repositories, err := coordinator.Store.Repositories(ctx)
	if err != nil {
		return err
	}
	for _, stored := range repositories {
		if err := ctx.Err(); err != nil {
			return err
		}
		policy, exists, err := coordinator.Store.CheckPolicy(ctx, stored.ID)
		if err != nil {
			return err
		}
		if !exists || !policy.ConsentActive || policy.ConsentDigest != policy.Digest || policy.Execution.Legacy {
			continue
		}
		if err := coordinator.reconcilePushes(ctx, stored.ID, policy); err != nil {
			coordinator.log("reconcile push checks for %s: %v", stored.ID, err)
		}
		if err := coordinator.reconcilePullRequests(ctx, stored.ID, policy); err != nil {
			coordinator.log("reconcile pull request checks for %s: %v", stored.ID, err)
		}
	}
	return nil
}

func (coordinator *Coordinator) reconcilePushes(ctx context.Context, repositoryID string, policy state.CheckPolicy) error {
	if coordinator.pushCursor == nil {
		coordinator.pushCursor = make(map[string]string)
	}
	summary, err := coordinator.Repositories.Summary(ctx, repositoryID)
	if err != nil {
		return err
	}
	sort.Slice(summary.Branches, func(i, j int) bool { return summary.Branches[i].Name < summary.Branches[j].Name })
	branches := boundedBranchesAfter(summary.Branches, coordinator.pushCursor[repositoryID], maximumObservedRefs)
	if len(summary.Branches) > maximumObservedRefs {
		coordinator.log("configured check ref reconciliation for %s is processing a fair batch of %d/%d branches", repositoryID, len(branches), len(summary.Branches))
	}
	observations, err := coordinator.Store.CheckObservations(ctx, repositoryID)
	if err != nil {
		return err
	}
	previous := make(map[string]string, len(observations))
	for _, observation := range observations {
		previous[observation.RefName] = observation.OID
	}
	live := make(map[string]bool, len(summary.Branches))
	for _, branch := range summary.Branches {
		live["refs/heads/"+branch.Name] = true
	}
	for _, branch := range branches {
		refName := "refs/heads/" + branch.Name
		if previous[refName] != branch.OID {
			admitted, err := coordinator.admit(ctx, policy, state.CheckJobRequest{
				RepositoryID: repositoryID, Trigger: checkworkflow.EventPush,
				EventKey: refName + "@" + branch.OID, SourceOID: branch.OID, TriggerRef: branch.Name,
			})
			switch {
			case errors.Is(err, errRevisionRejected):
				// Recording the observation below means this revision is
				// reported once and admitted again only after the branch moves.
				coordinator.log("configured check push %s %s at %s was not admitted: %v", repositoryID, branch.Name, branch.OID, err)
			case err != nil:
				return err
			case admitted || previous[refName] != "":
				coordinator.log("observed configured check push %s %s", repositoryID, branch.Name)
			}
		}
		if err := coordinator.Store.RecordCheckObservation(ctx, repositoryID, refName, branch.OID, time.Now().UTC()); err != nil {
			return err
		}
		coordinator.pushCursor[repositoryID] = branch.Name
	}
	if len(summary.Branches) == 0 {
		delete(coordinator.pushCursor, repositoryID)
	}
	for refName := range previous {
		if !live[refName] {
			if err := coordinator.Store.DeleteCheckObservation(ctx, repositoryID, refName); err != nil {
				return err
			}
		}
	}
	return nil
}

func (coordinator *Coordinator) reconcilePullRequests(ctx context.Context, repositoryID string, policy state.CheckPolicy) error {
	if coordinator.pullRequestCursor == nil {
		coordinator.pullRequestCursor = make(map[string]int64)
	}
	if !contains(policy.AllowedEvents, checkworkflow.EventPullRequest) {
		return nil
	}
	revisions, more, err := coordinator.PullRequests.ObserveCurrentRevisionsAfter(ctx, repositoryID, coordinator.pullRequestCursor[repositoryID], maximumObservedPRs)
	if err != nil {
		return err
	}
	observed := 0
	for _, revision := range revisions {
		coordinator.pullRequestCursor[repositoryID] = revision.PullRequest.Number
		if revision.NewlyObserved {
			observed++
		}
		if revision.SourceOID == "" || revision.TargetOID == "" {
			continue
		}
		_, err := coordinator.admit(ctx, policy, state.CheckJobRequest{
			RepositoryID: repositoryID, Trigger: checkworkflow.EventPullRequest,
			EventKey:  fmt.Sprintf("pr/%d/%s/%s", revision.PullRequest.Number, revision.SourceOID, revision.TargetOID),
			SourceOID: revision.SourceOID, BaseOID: revision.TargetOID, PullRequestNumber: revision.PullRequest.Number,
			TriggerRef: revision.PullRequest.TargetBranch,
		})
		if errors.Is(err, errRevisionRejected) {
			coordinator.log("configured check pull request %s #%d at %s was not admitted: %v", repositoryID, revision.PullRequest.Number, revision.SourceOID, err)
		} else if err != nil {
			return err
		}
	}
	if observed > 0 {
		coordinator.log("observed %d current pull request revisions for %s", observed, repositoryID)
	}
	if more {
		coordinator.log("configured check pull request reconciliation for %s is processing fair batches of at most %d open requests", repositoryID, maximumObservedPRs)
	}
	if len(revisions) == 0 {
		delete(coordinator.pullRequestCursor, repositoryID)
	}
	return nil
}

func (coordinator *Coordinator) admit(ctx context.Context, policy state.CheckPolicy, request state.CheckJobRequest) (bool, error) {
	if !contains(policy.AllowedEvents, request.Trigger) {
		return false, nil
	}
	pinned, err := coordinator.Repositories.PinRepository(ctx, request.RepositoryID, request.SourceOID, request.SourceOID)
	if err != nil {
		return false, fmt.Errorf("pin configured check source %s: %w", request.SourceOID, err)
	}
	blob, err := pinned.ReadBlob(ctx, repository.PinnedHead, checkworkflow.Path, 0, policy.Execution.Source.MetadataLimit,
		checkworkflow.MaximumBytes+1, checkworkflow.MaximumBytes+1)
	if errors.Is(err, repository.ErrPinnedPathNotFound) {
		return false, nil
	}
	if errors.Is(err, repository.ErrPinnedUnsupportedObject) || errors.Is(err, repository.ErrPinnedOutputLimit) {
		return false, fmt.Errorf("%w: read configured check workflow: %w", errRevisionRejected, err)
	}
	if err != nil {
		return false, fmt.Errorf("read configured check workflow: %w", err)
	}
	if blob.Symlink || (blob.Mode != "100644" && blob.Mode != "100755") || blob.HasMore || blob.Size != int64(len(blob.Content)) || len(blob.Content) > checkworkflow.MaximumBytes {
		return false, fmt.Errorf("%w: configured check workflow exceeds its source bound", errRevisionRejected)
	}
	document, err := checkworkflow.Parse(blob.Content)
	if err != nil {
		return false, fmt.Errorf("%w: parse configured check workflow: %w", errRevisionRejected, err)
	}
	effective, err := checkworkflow.Tighten(document, checkworkflow.OperatorPolicy{
		AllowedEvents: policy.AllowedEvents, MaxTimeoutMS: policy.MaxTimeoutMS,
		MaxOutputLimitBytes: policy.MaxOutputLimitBytes,
	})
	if err != nil {
		return false, fmt.Errorf("%w: %w", errRevisionRejected, err)
	}
	if !effective.MatchesBranch(request.Trigger, request.TriggerRef) {
		return false, nil
	}
	request.WorkflowPath = checkworkflow.Path
	request.WorkflowOID = blob.OID
	digest := sha256.Sum256(blob.Content)
	request.WorkflowDigest = fmt.Sprintf("%x", digest[:])
	request.TimeoutMS = effective.TimeoutMS
	request.OutputLimitBytes = effective.OutputLimitBytes
	request.Checks = make([]state.CheckDefinition, 0, len(document.Checks))
	for _, check := range document.Checks {
		request.Checks = append(request.Checks, state.CheckDefinition{Name: check.Name, Command: check.Command})
	}
	_, deduped, err := coordinator.Store.AdmitCheckJob(ctx, request, time.Now().UTC())
	if errors.Is(err, state.ErrInvalidCheckJob) {
		return false, fmt.Errorf("%w: %w", errRevisionRejected, err)
	}
	return !deduped && err == nil, err
}

func (coordinator *Coordinator) runOneLocal(ctx context.Context) error {
	repositories, err := coordinator.Store.Repositories(ctx)
	if err != nil {
		return err
	}
	for _, stored := range repositories {
		job, claimed, err := coordinator.Store.ClaimLocalCheckJob(ctx, stored.ID, time.Now().UTC())
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if err := coordinator.executeLocal(ctx, job); err != nil {
			return err
		}
		coordinator.Wake(stored.ID)
		return nil
	}
	return nil
}

func (coordinator *Coordinator) executeLocal(parent context.Context, job state.CheckJob) error {
	authority := state.CheckJobCompletionAuthority{
		JobID: job.ID, LeaseID: job.LeaseID, CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration,
	}
	jobContext, cancelJob := context.WithCancel(parent)
	watchDone := make(chan error, 1)
	go coordinator.watchLease(jobContext, cancelJob, job, authority, watchDone)
	watchStopped := false
	stopWatcher := func() error {
		if watchStopped {
			return nil
		}
		watchStopped = true
		cancelJob()
		return <-watchDone
	}
	defer stopWatcher()

	_, workspace, err := coordinator.workspace.PrepareJob(job.ID)
	var materialization *checksource.Result
	if err == nil {
		var pinned *repository.PinnedRepository
		pinned, err = coordinator.Repositories.PinRepository(jobContext, job.RepositoryID, job.SourceOID, job.SourceOID)
		if err == nil {
			err = func() error {
				var materializeErr error
				materialization, materializeErr = checksource.MaterializePinned(jobContext, pinned, repository.PinnedHead, workspace, checksource.Options{
					Limits: checksource.Limits{
						MaxEntries: job.Execution.Source.MaxEntries, MaxFileBytes: job.Execution.Source.MaxFileBytes,
						MaxTotalBytes: job.Execution.Source.MaxTotalBytes, MaxPathDepth: job.Execution.Source.MaxPathDepth,
						MaxPathBytes: job.Execution.Source.MaxPathBytes, MaxNameBytes: job.Execution.Source.MaxNameBytes,
						MetadataLimit: job.Execution.Source.MetadataLimit,
					},
					Timeout: time.Duration(job.Limits.TimeoutMS) * time.Millisecond,
				})
				return materializeErr
			}()
		}
	}
	if err != nil {
		_ = coordinator.workspace.RemoveJob(job.ID)
		status := state.CheckJobUnavailable
		if parent.Err() != nil {
			status = state.CheckJobInterrupted
		}
		_ = stopWatcher()
		_, recordErr := coordinator.Store.FailCheckJobBeforeStart(context.WithoutCancel(parent), authority, status, boundedSummary("Exact configured-check source is unavailable or workspace ownership is uncertain: "+err.Error()), time.Now().UTC())
		if recordErr == nil {
			coordinator.log("reported configured-check job %s unavailable: %v", job.ID, err)
		}
		return recordErr
	}

	cleanupWorkspace := func(results []checkexec.Result) []checkexec.Result {
		if err := coordinator.workspace.RemoveJob(job.ID); err != nil && len(results) != 0 {
			results[len(results)-1].Status = checkexec.StatusError
			results[len(results)-1].CleanupError = boundedSummary("remove private source workspace: " + err.Error())
		}
		return results
	}

	var protection string
	switch job.Executor {
	case state.CheckExecutorHost:
		protection = state.ProtectionHost
	case state.CheckExecutorContainer:
		if err := coordinator.containerPreflight(jobContext, job, workspace); err != nil {
			_ = coordinator.workspace.RemoveJob(job.ID)
			_ = stopWatcher()
			_, recordErr := coordinator.Store.FailCheckJobBeforeStart(context.WithoutCancel(parent), authority, state.CheckJobUnavailable, boundedSummary("Configured container runtime is unavailable: "+err.Error()), time.Now().UTC())
			if recordErr == nil {
				coordinator.log("reported configured-check job %s runtime unavailable: %v", job.ID, err)
			}
			return recordErr
		}
		protection = state.ProtectionContainer
	default:
		_ = coordinator.workspace.RemoveJob(job.ID)
		return errors.New("local worker claimed a nonlocal configured check")
	}

	started, err := coordinator.Store.StartCheckJob(jobContext, state.CheckJobStart{
		RepositoryID: job.RepositoryID, JobID: job.ID, LeaseID: job.LeaseID,
		CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration, Protection: protection,
	}, time.Now().UTC())
	if err != nil {
		_ = coordinator.workspace.RemoveJob(job.ID)
		return err
	}
	configuration, exists, err := coordinator.Store.CheckConfiguration(jobContext, job.RepositoryID, job.ConfigurationVersion)
	if err != nil || !exists {
		_ = coordinator.workspace.RemoveJob(job.ID)
		return errors.Join(err, errors.New("configured check job configuration is unavailable"))
	}
	attemptID, err := state.RandomID()
	if err != nil {
		_ = coordinator.workspace.RemoveJob(job.ID)
		return err
	}
	attempt := state.CheckAttempt{
		ID: attemptID, TaskID: job.TaskID, RepositoryID: job.RepositoryID, RevisionOID: job.SourceOID,
		WorktreeState: state.WorktreeClean, JobID: job.ID, Checks: configuration.Checks,
		StartedAt: *started.StartedAt, CreatedAt: time.Now().UTC(), CredentialID: job.CredentialID,
	}
	if _, _, err := coordinator.Store.RegisterCheckAttempt(jobContext, attempt); err != nil {
		_ = coordinator.workspace.RemoveJob(job.ID)
		return err
	}

	definitions := make([]checkexec.Definition, 0, len(configuration.Checks))
	for _, check := range configuration.Checks {
		definitions = append(definitions, checkexec.Definition{Name: check.Name, Command: check.Command})
	}
	var results []checkexec.Result
	var cancelled bool
	if job.Executor == state.CheckExecutorContainer {
		results, cancelled = coordinator.runContainerChecks(jobContext, job, workspace, definitions)
	} else {
		results, cancelled = checkexec.Run(jobContext, definitions, checkexec.Options{
			Dir: workspace, Timeout: time.Duration(job.Limits.TimeoutMS) * time.Millisecond,
			OutputLimit: job.Limits.OutputLimitBytes,
		})
	}
	submittedWorktree := state.WorktreeClean
	verifyContext, cancelVerify := context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
	clean, verifyErr := checksource.VerifyResult(verifyContext, materialization)
	cancelVerify()
	if verifyErr != nil {
		submittedWorktree = state.WorktreeUnknown
		if len(results) != 0 {
			results[len(results)-1].Status = checkexec.StatusError
			results[len(results)-1].CleanupError = boundedSummary("verify private source workspace: " + verifyErr.Error())
		}
	} else if !clean {
		submittedWorktree = state.WorktreeDirty
		if len(results) != 0 && results[len(results)-1].Status == checkexec.StatusPassed {
			results[len(results)-1].Status = checkexec.StatusIncomplete
		}
	}
	watchErr := stopWatcher()
	results = cleanupWorkspace(results)
	if watchErr != nil && !errors.Is(watchErr, context.Canceled) && len(results) != 0 {
		results[len(results)-1].Status = checkexec.StatusError
		results[len(results)-1].CleanupError = boundedSummary("lease or cancellation observation failed: " + watchErr.Error())
	}
	completion := state.CheckCompletion{
		AttemptID: attemptID, RepositoryID: job.RepositoryID, TaskID: job.TaskID,
		Results: stateResults(results), Cancelled: cancelled, FinishedAt: time.Now().UTC(),
		WorktreeState: submittedWorktree,
	}
	completion.Log, completion.LogTruncated = buildLog(results)
	_, _, completeErr := coordinator.Store.CompleteCheckJobAttempt(context.WithoutCancel(parent), completion, authority, time.Now().UTC())
	return completeErr
}

func (coordinator *Coordinator) watchLease(ctx context.Context, cancel context.CancelFunc, job state.CheckJob, authority state.CheckJobCompletionAuthority, done chan<- error) {
	interval := time.Second
	if job.LeaseExpiresAt != nil {
		remaining := time.Until(*job.LeaseExpiresAt)
		if candidate := remaining / 3; candidate > 100*time.Millisecond && candidate < interval {
			interval = candidate
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- ctx.Err()
			return
		case <-ticker.C:
			current, exists, err := coordinator.Store.CheckJob(ctx, job.RepositoryID, job.ID)
			if err != nil || !exists {
				cancel()
				if err == nil {
					err = state.ErrCheckJobNotFound
				}
				done <- err
				return
			}
			if current.CancelRequestedAt != nil {
				cancel()
				done <- nil
				return
			}
			if _, err := coordinator.Store.RenewCheckJobLease(ctx, authority, time.Now().UTC()); err != nil {
				cancel()
				done <- err
				return
			}
		}
	}
}

func stateResults(results []checkexec.Result) []state.CheckResult {
	converted := make([]state.CheckResult, 0, len(results))
	for index, result := range results {
		excerpt, cut := checkapi.ClipText(result.Output, state.MaximumCheckExcerptBytes)
		truncated := result.Truncated || cut
		cleanupError := result.CleanupError
		if cleanupError != "" {
			cleanupError = boundedSummary(cleanupError)
		}
		converted = append(converted, state.CheckResult{
			Position: index, Name: result.Name, Command: result.Command, Status: result.Status,
			ExitCode: result.ExitCode, DurationMS: result.Duration.Milliseconds(), OutputExcerpt: excerpt,
			Truncated: truncated, CleanupError: cleanupError,
		})
	}
	return converted
}

func buildLog(results []checkexec.Result) (string, bool) {
	log := checkapi.LogBuffer{Limit: maximumAutomaticLogSize}
	for _, result := range results {
		if !log.Add(fmt.Sprintf("[%s] %s\n%s\n", result.Status, result.Command, result.Output)) {
			break
		}
	}
	return log.Result()
}

func boundedBranchesAfter(branches []repository.Ref, after string, limit int) []repository.Ref {
	if len(branches) == 0 || limit < 1 {
		return nil
	}
	if limit > len(branches) {
		limit = len(branches)
	}
	start := sort.Search(len(branches), func(index int) bool { return branches[index].Name > after })
	if start == len(branches) {
		start = 0
	}
	selected := make([]repository.Ref, 0, limit)
	for offset := 0; offset < limit; offset++ {
		selected = append(selected, branches[(start+offset)%len(branches)])
	}
	return selected
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func boundedSummary(value string) string {
	value = checkapi.SummaryText(value, 500)
	if value == "" {
		return "Configured check execution failed before a command started."
	}
	return value
}

func (coordinator *Coordinator) log(format string, arguments ...any) {
	if coordinator.Logf != nil {
		coordinator.Logf(format, arguments...)
	}
}
