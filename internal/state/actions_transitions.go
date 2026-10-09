package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"owngit/internal/actions"
)

func readActionsJobsTx(ctx context.Context, queryer querier, runID string) ([]CheckJob, error) {
	rows, err := queryer.QueryContext(ctx, checkJobSelect+` WHERE run_id=? ORDER BY rowid`, runID)
	if err != nil {
		return nil, err
	}
	var jobs []CheckJob
	for rows.Next() {
		job, err := scanCheckJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, closeRows(rows)
}

func readActionsPlanTx(ctx context.Context, tx *sql.Tx, job CheckJob) (actions.JobPlan, error) {
	var encoded, digest string
	if err := tx.QueryRowContext(ctx, `SELECT plan_json,plan_digest FROM actions_job_plans WHERE job_id=?`, job.ID).Scan(&encoded, &digest); err != nil {
		return actions.JobPlan{}, err
	}
	if digest != job.PlanDigest {
		return actions.JobPlan{}, errors.New("workflow plan digest differs from its job")
	}
	if err := validateActionsPlan(json.RawMessage(encoded), job.JobKey, job.MatrixIndex, digest, 0); err != nil {
		return actions.JobPlan{}, err
	}
	return actions.DecodePlan([]byte(encoded), digest)
}

func settleActionsRunsTx(ctx context.Context, tx *sql.Tx, now time.Time, runID string) error {
	if runID != "" {
		if err := settleActionsRunTx(ctx, tx, now, runID); err != nil {
			return err
		}
	}
	for {
		var before, after int64
		if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&before); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT run_id FROM check_jobs j WHERE run_id!='' AND (status='waiting' OR (status IN ('pending','claimed','started') AND EXISTS (SELECT 1 FROM actions_runs r WHERE r.id=j.run_id AND r.cancel_requested_at IS NOT NULL)) OR (status NOT IN ('pending','waiting','claimed','started') AND EXISTS (SELECT 1 FROM actions_job_plans p WHERE p.job_id=j.id)))`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := closeRows(rows); err != nil {
			return err
		}
		for _, id := range ids {
			if err := settleActionsRunTx(ctx, tx, now, id); err != nil {
				return err
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&after); err != nil {
			return err
		}
		if before == after {
			return nil
		}
	}
}

func settleActionsRunTx(ctx context.Context, tx *sql.Tx, now time.Time, id string) error {
	run, exists, err := readActionsRunTx(ctx, tx, actionsRunSelect+` WHERE id=?`, id)
	if err != nil {
		return err
	}
	if !exists {
		return ErrInvalidActionsRun
	}
	jobs, err := readActionsJobsTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := validateActionsGraph(run, jobs); err != nil {
		return err
	}
	if run.CancelRequestedAt != nil {
		for _, job := range jobs {
			if _, err := cancelCheckJobTx(ctx, tx, job, now, "The workflow run was cancelled."); err != nil {
				return err
			}
		}
	} else {
		policy, _, err := readCheckPolicyTx(ctx, tx, run.RepositoryID)
		if err != nil {
			return err
		}
		for {
			jobs, err = readActionsJobsTx(ctx, tx, id)
			if err != nil {
				return err
			}
			changed := false
			for _, job := range jobs {
				if !run.Facts.FailFast[job.JobKey] || !actions.FailFastFailure(job.Status, job.Tolerated) {
					continue
				}
				for index, sibling := range jobs {
					if sibling.ID == job.ID || sibling.JobKey != job.JobKey {
						continue
					}
					summary := fmt.Sprintf("note.fail_fast: Cancelled because %s failed and fail-fast is on. Set fail-fast: false to run every combination.", job.JobKey)
					cancelled, err := cancelCheckJobTx(ctx, tx, sibling, now, summary)
					if err != nil {
						return err
					}
					if sibling.Status != cancelled.Status || sibling.CancelRequestedAt == nil && cancelled.CancelRequestedAt != nil {
						changed = true
					}
					jobs[index] = cancelled
				}
			}
			for _, job := range jobs {
				if job.Status != CheckJobWaiting {
					continue
				}
				if !checkJobAuthorityCurrent(job, policy) {
					if err := finishUnstartedActionsJobTx(ctx, tx, job, CheckJobInterrupted, "Execution authority changed before start.", now); err != nil {
						return err
					}
					changed = true
					break
				}
				plan, err := readActionsPlanTx(ctx, tx, job)
				if err != nil {
					if err := finishUnstartedActionsJobTx(ctx, tx, job, CheckJobError, "workflow.plan: The stored workflow plan could not be read.", now); err != nil {
						return err
					}
					changed = true
					break
				}
				plan.Context.GitHub.RunID, plan.Context.GitHub.RunNumber, plan.Context.GitHub.RunAttempt = run.ID, run.Number, run.RerunGeneration+1
				resolved, err := actions.ResolveJob(plan, actionsDependencies(run, jobs))
				if err != nil {
					if err := finishUnstartedActionsJobTx(ctx, tx, job, CheckJobError, boundedActionsSummary(err.Error()), now); err != nil {
						return err
					}
					changed = true
					break
				}
				if resolved.Status == actions.StatusWaiting {
					continue
				}
				if resolved.Status == actions.StatusSkipped {
					summary := "The job condition prevented execution."
					if ancestor := ambiguousAncestor(run, jobs, job.JobKey); ancestor != "" {
						summary = boundedActionsSummary("note.uncertain: Execution of " + ancestor + " is uncertain. Jobs that need it were skipped. Rerun the run when that is safe.")
					}
					if err := finishUnstartedActionsJobTx(ctx, tx, job, CheckJobSkipped, summary, now); err != nil {
						return err
					}
				} else {
					if err := releaseActionsJobTx(ctx, tx, job, resolved); err != nil {
						return err
					}
					job.Status, job.ConcurrencyGroup = CheckJobPending, resolved.Concurrency.Group
					if err := admitActionsJobConcurrencyTx(ctx, tx, run, job, resolved.Concurrency, now); err != nil {
						return err
					}
				}
				changed = true
				break
			}
			if !changed {
				break
			}
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM actions_job_plans WHERE job_id IN (SELECT id FROM check_jobs WHERE run_id=? AND status NOT IN ('pending','waiting','claimed','started'))`, id)
	return err
}

func actionsDependencies(run ActionsRun, jobs []CheckJob) map[string][]actions.Dependency {
	dependencies := map[string][]actions.Dependency{}
	for _, job := range jobs {
		dependencies[job.JobKey] = append(dependencies[job.JobKey], actions.Dependency{Status: job.Status, Tolerated: job.Tolerated})
	}
	for _, refused := range run.Facts.RefusedJobs {
		dependencies[refused.JobKey] = append(dependencies[refused.JobKey], actions.Dependency{Status: actions.StatusRefused})
	}
	ancestorFailures, resolved := map[string]bool{}, map[string]bool{}
	var ancestorFailed func(string) bool
	ancestorFailed = func(key string) (failed bool) {
		if resolved[key] {
			return ancestorFailures[key]
		}
		defer func() { ancestorFailures[key], resolved[key] = failed, true }()
		for _, needed := range run.Facts.Needs[key] {
			for _, dependency := range dependencies[needed] {
				if actions.NeedsResult(dependency.Status, dependency.Tolerated) == actions.ResultFailure {
					return true
				}
			}
			if ancestorFailed(needed) {
				return true
			}
		}
		return false
	}
	for key, items := range dependencies {
		failed := ancestorFailed(key)
		for i := range items {
			items[i].AncestorFailed = failed
			items[i].AncestorAmbiguous = ambiguousAncestor(run, jobs, key) != ""
		}
	}
	return dependencies
}

func ambiguousAncestor(run ActionsRun, jobs []CheckJob, key string) string {
	ambiguous := map[string]bool{}
	for _, job := range jobs {
		if job.Status == CheckJobAmbiguous {
			ambiguous[job.JobKey] = true
		}
	}
	visited := map[string]bool{}
	var find func(string) string
	find = func(key string) string {
		if visited[key] {
			return ""
		}
		visited[key] = true
		for _, needed := range run.Facts.Needs[key] {
			if ambiguous[needed] {
				return needed
			}
			if ancestor := find(needed); ancestor != "" {
				return ancestor
			}
		}
		return ""
	}
	return find(key)
}

func releaseActionsJobTx(ctx context.Context, tx *sql.Tx, job CheckJob, resolved actions.PlannedJob) error {
	configuration, exists, err := readCheckConfigurationTx(ctx, tx, job.RepositoryID, job.ConfigurationVersion)
	if err != nil {
		return err
	}
	if !exists {
		return ErrCheckConfigurationMissing
	}
	job.PlanDigest, job.Tolerated, job.ConcurrencyGroup = resolved.Digest, resolved.Tolerated, resolved.Concurrency.Group
	job.MaxParallel = min(resolved.Plan.Context.Strategy.MaxParallel, MaximumActionsRunJobs)
	job.DedupDigest = checkJobDedupDigest(job, configuration.ConfigHash)
	if _, err := tx.ExecContext(ctx, `UPDATE actions_job_plans SET plan_json=?,plan_digest=? WHERE job_id=?`, string(resolved.Encoded), resolved.Digest, job.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE check_jobs SET status='pending',plan_digest=?,tolerated=?,concurrency_group=?,max_parallel=?,dedup_digest=? WHERE id=? AND status='waiting'`, job.PlanDigest, boolInt(job.Tolerated), job.ConcurrencyGroup, job.MaxParallel, job.DedupDigest, job.ID)
	return err
}

func finishUnstartedActionsJobTx(ctx context.Context, tx *sql.Tx, job CheckJob, status, summary string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE check_jobs SET status=?,finished_at=?,summary=? WHERE id=? AND status IN ('pending','waiting','claimed')`, status, now.UTC().UnixNano(), summary, job.ID)
	return err
}

func cancelCheckJobTx(ctx context.Context, tx *sql.Tx, job CheckJob, now time.Time, summary string) (CheckJob, error) {
	when := now.UTC()
	switch job.Status {
	case CheckJobPending, CheckJobWaiting, CheckJobClaimed:
		_, err := tx.ExecContext(ctx, `UPDATE check_jobs SET status='cancelled',cancel_requested_at=?,finished_at=?,summary=? WHERE id=? AND status IN ('pending','waiting','claimed')`, when.UnixNano(), when.UnixNano(), summary, job.ID)
		if err != nil {
			return CheckJob{}, err
		}
		job.Status, job.FinishedAt = CheckJobCancelled, &when
	case CheckJobStarted, CheckJobAmbiguous:
		if job.CancelRequestedAt != nil {
			return job, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE check_jobs SET cancel_requested_at=?,summary=? WHERE id=?`, when.UnixNano(), summary, job.ID); err != nil {
			return CheckJob{}, err
		}
	default:
		return job, nil
	}
	job.CancelRequestedAt, job.Summary = &when, summary
	return job, nil
}

func (s *Store) CancelActionsRun(ctx context.Context, repositoryID, runID string, now time.Time) (ActionsRun, error) {
	if repositoryID == "" || !validAttemptID(runID) || now.IsZero() {
		return ActionsRun{}, ErrInvalidActionsRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ActionsRun{}, err
	}
	defer tx.Rollback()
	run, exists, err := readActionsRunTx(ctx, tx, actionsRunSelect+` WHERE repository_id=? AND id=?`, repositoryID, runID)
	if err != nil {
		return ActionsRun{}, err
	}
	if !exists {
		return ActionsRun{}, ErrInvalidActionsRun
	}
	if err := cancelActionsRunTx(ctx, tx, run, now, "The workflow run was cancelled."); err != nil {
		return ActionsRun{}, err
	}
	if err := settleActionsRunsTx(ctx, tx, now, run.ID); err != nil {
		return ActionsRun{}, err
	}
	run, _, err = readActionsRunTx(ctx, tx, actionsRunSelect+` WHERE id=?`, run.ID)
	if err != nil {
		return ActionsRun{}, err
	}
	return run, tx.Commit()
}

func cancelActionsRunTx(ctx context.Context, tx *sql.Tx, run ActionsRun, now time.Time, summary string) error {
	jobs, err := readActionsJobsTx(ctx, tx, run.ID)
	if err != nil {
		return err
	}
	unfinished := false
	for _, job := range jobs {
		unfinished = unfinished || !terminalCheckJob(job.Status) || job.Status == CheckJobAmbiguous
	}
	if !unfinished {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE actions_runs SET cancel_requested_at=COALESCE(cancel_requested_at,?) WHERE id=?`, now.UTC().UnixNano(), run.ID); err != nil {
		return err
	}
	for _, job := range jobs {
		if _, err := cancelCheckJobTx(ctx, tx, job, now, summary); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ReconcileActionsJobs(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return ErrInvalidActionsRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := settleActionsRunsTx(ctx, tx, now, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func boundedActionsSummary(text string) string {
	text = strings.Map(func(r rune) rune {
		if r == '\x00' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, text)
	if len(text) > 500 {
		text = text[:500]
		for !validText(text, 500) && len(text) > 0 {
			text = text[:len(text)-1]
		}
	}
	return text
}
