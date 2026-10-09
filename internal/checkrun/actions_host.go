package checkrun

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"owngit/internal/actions"
	"owngit/internal/actions/host"
	"owngit/internal/checkapi"
	"owngit/internal/checksource"
	"owngit/internal/state"
)

func (coordinator *Coordinator) executeLocalActions(parent context.Context, job state.CheckJob) error {
	authority := state.CheckJobCompletionAuthority{JobID: job.ID, LeaseID: job.LeaseID, CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration}
	ctx, cancel := context.WithCancel(parent)
	watchDone := make(chan error, 1)
	go coordinator.watchLease(ctx, cancel, job, authority, watchDone)
	watchStopped := false
	stopWatcher := func() error {
		if watchStopped {
			return nil
		}
		watchStopped = true
		cancel()
		return <-watchDone
	}
	defer stopWatcher()
	failBeforeStart := func(code, reason string) error {
		_ = stopWatcher()
		if coordinator.workspace != nil {
			_ = coordinator.workspace.RemoveJob(job.ID)
		}
		status := state.CheckJobError
		if parent.Err() != nil {
			status = state.CheckJobInterrupted
		}
		_, err := coordinator.Store.FailCheckJobBeforeStart(context.WithoutCancel(parent), authority, status, boundedSummary(code+": "+reason), time.Now().UTC())
		return err
	}
	encoded, exists, err := coordinator.Store.ActionsJobPlan(ctx, job.RepositoryID, job.ID)
	if err != nil || !exists {
		return failBeforeStart("workflow.plan", "The stored workflow plan could not be read, so this job did not start.")
	}
	plan, err := actions.DecodePlan(encoded, job.PlanDigest)
	if err != nil {
		return failBeforeStart("workflow.plan", "The stored workflow plan is invalid, so this job did not start.")
	}
	run, exists, err := coordinator.Store.ActionsRun(ctx, job.RepositoryID, job.RunID)
	if err != nil || !exists {
		return failBeforeStart("workflow.plan", "The workflow run could not be read, so this job did not start.")
	}
	secrets, err := coordinator.Store.ReadWorkflowSecrets(ctx, job.RepositoryID, plan.SecretNames)
	if err != nil {
		return failBeforeStart("workflow.secrets_unreadable", "The secrets of this repository could not be read, so this job did not start. An administrator can check them on the Secrets page.")
	}
	if job.Executor != state.CheckExecutorHost {
		return failBeforeStart("workflow.executor", "Local workflow execution requires the host executor in this build.")
	}
	plan.Context.GitHub.RunID, plan.Context.GitHub.RunNumber, plan.Context.GitHub.RunAttempt = run.ID, run.Number, run.RerunGeneration+1
	if err := executionRunsOn(&plan); err != nil {
		return failBeforeStart("workflow.plan", "The executor labels could not be evaluated, so this job did not start.")
	}
	workspace, materialization, err := coordinator.materialize(ctx, job)
	if err != nil {
		return failBeforeStart("workflow.source", "The exact workflow source or private workspace is unavailable, so this job did not start.")
	}
	attemptID, err := state.RandomID()
	if err != nil {
		return failBeforeStart("workflow.start", "An attempt identity could not be allocated, so this job did not start.")
	}
	_, attempt, err := coordinator.Store.StartCheckJob(ctx, state.CheckJobStart{RepositoryID: job.RepositoryID, JobID: job.ID, LeaseID: job.LeaseID, CredentialID: job.CredentialID, CredentialGeneration: job.CredentialGeneration, Protection: state.ProtectionHost, AttemptID: attemptID}, time.Now().UTC())
	var notStarted *state.CheckJobNotStartedError
	if errors.As(err, &notStarted) {
		return failBeforeStart("workflow.start", "The execution grant was not recorded, so this job did not start.")
	}
	if err != nil {
		_ = coordinator.workspace.RemoveJob(job.ID)
		return err
	}
	result := host.RunJob(ctx, plan, actions.RunOptions{Workspace: workspace, Directory: filepath.Join(filepath.Dir(workspace), "actions"), Identity: actions.RunIdentity{ID: run.ID, Number: run.Number, Attempt: run.RerunGeneration + 1}, Secrets: secrets, Evaluator: workflowEvaluator, MaxTimeout: time.Duration(job.Limits.TimeoutMS) * time.Millisecond, OutputLimit: job.Limits.OutputLimitBytes}, coordinator.Repositories.Git.GitPath)
	results := actionsStateResults(attempt, result)
	worktree := state.WorktreeClean
	verifyContext, cancelVerify := context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
	clean, verifyErr := checksource.VerifyResult(verifyContext, materialization)
	cancelVerify()
	if verifyErr != nil {
		worktree = state.WorktreeUnknown
		setActionsCleanupError(results, "Private source workspace verification could not be confirmed.")
	} else if !clean {
		worktree = state.WorktreeDirty
		facts := make([]actions.StepEvidence, len(results))
		for index, result := range results {
			facts[index] = actions.StepEvidence{Status: result.Status, Role: result.Role, CleanupError: result.CleanupError}
		}
		if index := actions.MarkSourceChanged(facts); index >= 0 {
			results[index].Status, results[index].Role = facts[index].Status, facts[index].Role
		}
	}
	watchErr := stopWatcher()
	if err := coordinator.workspace.RemoveJob(job.ID); err != nil {
		setActionsCleanupError(results, "Private source workspace cleanup could not be confirmed.")
	}
	if watchErr != nil && !errors.Is(watchErr, context.Canceled) {
		setActionsCleanupError(results, "Execution lease or cancellation observation could not be confirmed.")
	}
	completion := state.CheckCompletion{AttemptID: attemptID, RepositoryID: job.RepositoryID, TaskID: job.TaskID, Results: results, Cancelled: result.Cancelled, FinishedAt: time.Now().UTC(), WorktreeState: worktree}
	completion.Log, completion.LogTruncated = actionsLog(result)
	completeContext, cancelComplete := context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
	defer cancelComplete()
	_, _, err = coordinator.Store.CompleteCheckJobAttempt(completeContext, completion, authority, time.Now().UTC())
	return err
}

func workflowEvaluator(key, text string, contexts map[string]any) (any, error) {
	success, _ := contexts["success"].(bool)
	failure, _ := contexts["failure"].(bool)
	cancelled, _ := contexts["cancelled"].(bool)
	context := actions.EvalContext{Values: contexts, Success: success, Failure: failure, Cancelled: cancelled}
	if key == "step.if" {
		return actions.Eval(text, key, context)
	}
	return actions.EvalTemplate(text, key, context)
}

func executionRunsOn(plan *actions.JobPlan) error {
	context, err := actions.ContextFromPlan(plan.Context)
	if err != nil {
		return err
	}
	var labels []string
	for _, text := range plan.RunsOn {
		value, err := actions.EvalTemplate(text, "job.runs-on", context)
		if err != nil {
			return err
		}
		items, multiple := value.([]any)
		if !multiple {
			items = []any{value}
		}
		for _, item := range items {
			label, err := actions.ScalarString(item)
			if err != nil {
				return err
			}
			labels = append(labels, label)
		}
	}
	plan.RunsOn = labels
	return nil
}

func actionsStateResults(attempt state.CheckAttempt, result actions.JobResult) []state.CheckResult {
	results := make([]state.CheckResult, len(attempt.Checks))
	for index, check := range attempt.Checks {
		results[index] = state.CheckResult{Position: index, Name: check.Name, Command: check.Command, Status: actions.StatusSkipped, Role: actions.RoleRun}
	}
	facts := result.Evidence()
	for index, step := range result.Steps {
		if step.Index < 0 || step.Index >= len(results) {
			continue
		}
		stored := &results[step.Index]
		stored.Status, stored.Role, stored.ExitCode = facts[index].Status, facts[index].Role, step.ExitCode
		stored.DurationMS, stored.CleanupError = step.Duration.Milliseconds(), step.CleanupError
		if stored.CleanupError != "" {
			stored.CleanupError = boundedSummary(stored.CleanupError)
		}
		var cut bool
		stored.OutputExcerpt, cut = checkapi.ClipLog(step.Output, state.MaximumCheckExcerptBytes, step.OutputGap)
		stored.Truncated = step.Truncated || cut
	}
	if result.Error != "" && len(results) != 0 {
		results[0].Status = actions.StatusError
		results[0].OutputExcerpt, results[0].Truncated = checkapi.ClipText(result.Error, state.MaximumCheckExcerptBytes)
	}
	return results
}

func setActionsCleanupError(results []state.CheckResult, reason string) {
	if len(results) == 0 {
		return
	}
	last := &results[len(results)-1]
	last.Status, last.Role, last.CleanupError = actions.StatusError, actions.RoleRun, reason
}

func actionsLog(result actions.JobResult) (string, bool) {
	log := checkapi.LogBuffer{Limit: maximumAutomaticLogSize}
	for _, note := range result.Notes {
		log.Add(note.Code + ": " + note.Detail + "\n")
	}
	if result.Error != "" {
		log.Add(result.Error + "\n")
	}
	truncated := false
	for _, step := range result.Steps {
		log.Add("\n=== " + step.Name + " ===\n")
		log.AddClipped(step.Output, step.OutputGap)
		log.Add("\n")
		for _, note := range step.Notes {
			log.Add(note.Code + ": " + note.Detail + "\n")
		}
		truncated = truncated || step.Truncated
	}
	text, cut := log.Result()
	return strings.TrimSpace(text), truncated || cut
}
