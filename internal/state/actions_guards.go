package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"owngit/internal/actions"
)

const actionsRunClaimGuard = `(run_id='' OR EXISTS (SELECT 1 FROM actions_runs r WHERE r.id=check_jobs.run_id AND r.cancel_requested_at IS NULL))`
const actionsOwnRunHeld = `EXISTS (SELECT 1 FROM check_jobs held WHERE held.run_id=check_jobs.run_id AND held.claimed_at IS NOT NULL)`
const actionsOwnJobGroupHeld = `(check_jobs.status IN ('claimed','started') OR EXISTS (
	SELECT 1 FROM actions_runs own WHERE own.id=check_jobs.run_id AND own.concurrency_group=check_jobs.concurrency_group COLLATE NOCASE
	AND ` + actionsOwnRunHeld + `))`
const actionsWorkflowClaimGuard = `(run_id='' OR NOT EXISTS (
	SELECT 1 FROM actions_runs own WHERE own.id=check_jobs.run_id AND own.concurrency_group!='' AND (
		EXISTS (SELECT 1 FROM actions_runs r WHERE r.repository_id=check_jobs.repository_id AND r.id!=own.id
			AND r.concurrency_group=own.concurrency_group COLLATE NOCASE
			AND EXISTS (SELECT 1 FROM check_jobs u WHERE u.run_id=r.id AND u.status IN ('pending','waiting','claimed','started'))
			AND (EXISTS (SELECT 1 FROM check_jobs h WHERE h.run_id=r.id AND h.claimed_at IS NOT NULL)
				OR (NOT ` + actionsOwnRunHeld + ` AND r.rowid<own.rowid)))
		OR EXISTS (SELECT 1 FROM check_jobs holder WHERE holder.repository_id=check_jobs.repository_id AND holder.run_id!=own.id
			AND holder.concurrency_group=own.concurrency_group COLLATE NOCASE
			AND (holder.status IN ('claimed','started') OR (holder.status='pending' AND NOT ` + actionsOwnRunHeld + ` AND holder.rowid<check_jobs.rowid)))
	)
))`
const actionsJobClaimGuard = `(concurrency_group='' OR (
	NOT EXISTS (SELECT 1 FROM check_jobs holder WHERE holder.repository_id=check_jobs.repository_id AND holder.id!=check_jobs.id
		AND holder.concurrency_group=check_jobs.concurrency_group COLLATE NOCASE
		AND (holder.status IN ('claimed','started') OR (holder.status='pending' AND NOT ` + actionsOwnJobGroupHeld + ` AND holder.rowid<check_jobs.rowid)))
	AND NOT EXISTS (SELECT 1 FROM actions_runs r WHERE r.repository_id=check_jobs.repository_id AND r.id!=check_jobs.run_id
		AND r.concurrency_group=check_jobs.concurrency_group COLLATE NOCASE
		AND EXISTS (SELECT 1 FROM check_jobs u WHERE u.run_id=r.id AND u.status IN ('pending','waiting','claimed','started'))
		AND (EXISTS (SELECT 1 FROM check_jobs h WHERE h.run_id=r.id AND h.claimed_at IS NOT NULL)
			OR (NOT ` + actionsOwnJobGroupHeld + ` AND EXISTS (SELECT 1 FROM check_jobs earlier WHERE earlier.run_id=r.id AND earlier.rowid<check_jobs.rowid))))
))`
const actionsParallelClaimGuard = `(run_id='' OR max_parallel=0 OR max_parallel>(
	SELECT COUNT(*) FROM check_jobs sibling WHERE sibling.run_id=check_jobs.run_id
	AND sibling.job_key=check_jobs.job_key AND sibling.id!=check_jobs.id AND sibling.status IN ('claimed','started')
))`

func noteActionsClaimGuardsTx(ctx context.Context, tx *sql.Tx, repositoryID string, workflows bool) error {
	_, err := tx.ExecContext(ctx, `UPDATE check_jobs SET summary=trim((CASE
		WHEN NOT `+actionsWorkflowClaimGuard+` OR NOT `+actionsJobClaimGuard+` THEN 'note.concurrency_wait: Waiting for concurrency group '||COALESCE(NULLIF(concurrency_group,''),(SELECT concurrency_group FROM actions_runs r WHERE r.id=check_jobs.run_id))||'.'
		WHEN NOT `+actionsParallelClaimGuard+` THEN 'note.max_parallel: Waiting: max-parallel allows '||max_parallel||' jobs of this matrix at a time.'
		ELSE '' END) || CASE WHEN ? THEN '' ELSE ' ' || ? END) WHERE repository_id=? AND run_id!='' AND status='pending'`, workflows, runnerOldNote, repositoryID)
	return err
}

func actionsStartAllowedTx(ctx context.Context, tx *sql.Tx, job CheckJob) (bool, error) {
	if job.RunID == "" {
		return true, nil
	}
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT `+actionsRunClaimGuard+` AND `+actionsWorkflowClaimGuard+` AND `+actionsJobClaimGuard+` AND `+actionsParallelClaimGuard+` FROM check_jobs WHERE id=?`, job.ID).Scan(&allowed)
	return allowed, err
}

func admitActionsRunConcurrencyTx(ctx context.Context, tx *sql.Tx, run ActionsRun, now time.Time) error {
	if run.Outcome != "" {
		return nil
	}
	return applyActionsConcurrencyTx(ctx, tx, run, nil, actions.EvaluatedConcurrency{Group: run.ConcurrencyGroup, Queue: run.ConcurrencyQueue, CancelInProgress: run.CancelInProgress}, now)
}

func admitActionsJobConcurrencyTx(ctx context.Context, tx *sql.Tx, run ActionsRun, job CheckJob, concurrency actions.EvaluatedConcurrency, now time.Time) error {
	return applyActionsConcurrencyTx(ctx, tx, run, &job, concurrency, now)
}

func applyActionsConcurrencyTx(ctx context.Context, tx *sql.Tx, run ActionsRun, job *CheckJob, concurrency actions.EvaluatedConcurrency, now time.Time) error {
	if concurrency.Group == "" {
		return nil
	}
	runs, err := readActionsRuns(ctx, tx, actionsRunSelect+` WHERE repository_id=? AND concurrency_group=? COLLATE NOCASE AND id!=? ORDER BY rowid`, run.RepositoryID, concurrency.Group, run.ID)
	if err != nil {
		return err
	}
	owners, pending := map[string]bool{}, 0
	summary := boundedActionsSummary(fmt.Sprintf("note.concurrency_cancel: Cancelled by run #%d in concurrency group %q.", run.Number, concurrency.Group))
	for _, other := range runs {
		jobs, err := readActionsJobsTx(ctx, tx, other.ID)
		if err != nil {
			return err
		}
		active, held := false, false
		for _, candidate := range jobs {
			active = active || !terminalCheckJob(candidate.Status)
			held = held || candidate.ClaimedAt != nil
		}
		if !active {
			continue
		}
		owners[other.ID] = true
		if held && !concurrency.CancelInProgress {
			continue
		}
		if !held {
			pending++
			if concurrency.Queue == "max" {
				continue
			}
		}
		if err := cancelActionsRunTx(ctx, tx, other, now, summary); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, checkJobSelect+` WHERE repository_id=? AND concurrency_group=? COLLATE NOCASE AND status IN ('pending','claimed','started') ORDER BY rowid`, run.RepositoryID, concurrency.Group)
	if err != nil {
		return err
	}
	var others []CheckJob
	for rows.Next() {
		other, err := scanCheckJob(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if owners[other.RunID] || job == nil && other.RunID == run.ID || job != nil && other.ID == job.ID {
			continue
		}
		others = append(others, other)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	for _, other := range others {
		if other.Status == CheckJobPending {
			pending++
			if concurrency.Queue == "max" {
				continue
			}
		} else if !concurrency.CancelInProgress {
			continue
		}
		if _, err := cancelCheckJobTx(ctx, tx, other, now, summary); err != nil {
			return err
		}
	}
	if concurrency.Queue == "max" && pending >= 100 {
		if job == nil {
			return cancelActionsRunTx(ctx, tx, run, now, "note.concurrency_cancel: The concurrency group already has 100 pending runs or jobs.")
		}
		_, err = cancelCheckJobTx(ctx, tx, *job, now, "note.concurrency_cancel: The concurrency group already has 100 pending runs or jobs.")
	}
	return err
}
