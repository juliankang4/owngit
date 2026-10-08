package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func readActionsRecovery(ctx context.Context, tx *sql.Tx, snapshot *RecoveryState) error {
	runs, err := readActionsRuns(ctx, tx, actionsRunSelect+` ORDER BY repository_id,workflow_path,number`)
	if err != nil {
		return err
	}
	snapshot.ActionsRuns = runs
	return nil
}

func restoreActionsRecovery(ctx context.Context, tx *sql.Tx, snapshot RecoveryState) error {
	for _, run := range snapshot.ActionsRuns {
		if err := insertActionsRunTx(ctx, tx, run); err != nil {
			return fmt.Errorf("restore workflow run %q: %w", run.ID, err)
		}
	}
	return nil
}

func validatePortableActionsRuns(snapshot RecoveryState, repositories map[string]bool, policies map[string]CheckPolicy) (map[string]ActionsRun, error) {
	runs := make(map[string]ActionsRun, len(snapshot.ActionsRuns))
	numbers, events := make(map[string]bool), make(map[string]bool)
	for _, run := range snapshot.ActionsRuns {
		if err := validateActionsRun(run); err != nil {
			return nil, err
		}
		policy, exists := policies[run.RepositoryID]
		if !repositories[run.RepositoryID] || !exists || run.PolicyVersion > policy.Version || run.ConsentVersion > policy.ConsentVersion || run.PolicyVersion == policy.Version && (!policy.RunWorkflows || !policyAllowsCheckEvent(policy, run.Event)) {
			return nil, errors.New("workflow run policy generation is invalid")
		}
		number := fmt.Sprintf("%s\x00%s\x00%d", run.RepositoryID, run.WorkflowPath, run.Number)
		event := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d", run.RepositoryID, run.WorkflowPath, run.Event, run.EventKey, run.RerunGeneration)
		if runs[run.ID].ID != "" || numbers[number] || events[event] {
			return nil, errors.New("duplicate workflow run identity")
		}
		runs[run.ID], numbers[number], events[event] = run, true, true
	}
	for _, run := range runs {
		if run.RerunRoot == "" {
			continue
		}
		root, exists := runs[run.RerunRoot]
		if !exists || !sameActionsRunRoot(run, root) {
			return nil, errors.New("workflow rerun root is unknown or does not match its event")
		}
	}
	return runs, nil
}

func validateActionsJobRun(job CheckJob, run ActionsRun) error {
	if job.RepositoryID != run.RepositoryID || job.WorkflowPath != run.WorkflowPath || job.Trigger != run.Event || job.EventKey != run.EventKey || job.SourceOID != run.SourceOID || job.BaseOID != run.BaseOID || job.PullRequestNumber != run.PullRequestNumber || job.TriggerRef != run.TriggerRef || job.PolicyVersion != run.PolicyVersion || job.ConsentVersion != run.ConsentVersion || job.RerunRoot != run.RerunRoot || job.RerunGeneration != run.RerunGeneration || run.Outcome != "" {
		return errors.New("workflow job facts do not match its run")
	}
	if run.Facts.WorkflowDigest != "" && job.WorkflowDigest != run.Facts.WorkflowDigest || run.Facts.WorkflowOID != "" && job.WorkflowOID != run.Facts.WorkflowOID {
		return errors.New("workflow job file does not match its run")
	}
	return nil
}
