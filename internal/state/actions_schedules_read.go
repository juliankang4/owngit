package state

import (
	"context"
	"database/sql"
	"time"

	"owngit/internal/actions"
)

// ActionsSchedule is a machine-local schedule view. Paused covers unfinished
// or uncertain execution. Missing push authority is a separate note.
type ActionsSchedule struct {
	RepositoryID   string            `json:"repository_id"`
	WorkflowPath   string            `json:"workflow_path"`
	Cron           string            `json:"cron"`
	SourceOID      string            `json:"source_oid"`
	NextDueAt      time.Time         `json:"next_due_at"`
	LastSlotAt     *time.Time        `json:"last_slot_at,omitempty"`
	LastAdmittedAt *time.Time        `json:"last_admitted_at,omitempty"`
	LastRunID      string            `json:"last_run_id,omitempty"`
	UpdatedAt      time.Time         `json:"updated_at"`
	Paused         bool              `json:"paused"`
	PauseNote      string            `json:"pause_note,omitempty"`
	Notes          []actions.Message `json:"notes,omitempty"`
}

const actionsScheduleSelect = `SELECT repository_id,workflow_path,cron,source_oid,next_due_at,last_slot_at,last_admitted_at,last_run_id,updated_at FROM actions_schedules`

// ActionsSchedules lists one repository's schedules in file and cron order.
// defaultRef is the full current default branch ref, not a short branch name.
func (s *Store) ActionsSchedules(ctx context.Context, repositoryID, defaultRef string) ([]ActionsSchedule, error) {
	return s.actionsScheduleViews(ctx, repositoryID, "", defaultRef)
}

// ActionsSchedulesForWorkflow lists one workflow file's schedule entries.
func (s *Store) ActionsSchedulesForWorkflow(ctx context.Context, repositoryID, workflowPath, defaultRef string) ([]ActionsSchedule, error) {
	return s.actionsScheduleViews(ctx, repositoryID, workflowPath, defaultRef)
}

func (s *Store) actionsScheduleViews(ctx context.Context, repositoryID, workflowPath, defaultRef string) ([]ActionsSchedule, error) {
	schedules, err := readActionsSchedules(ctx, s.db, repositoryID)
	if err != nil {
		return nil, err
	}
	views := make([]ActionsSchedule, 0, len(schedules))
	for _, entry := range schedules {
		if workflowPath != "" && workflowPath != entry.WorkflowPath {
			continue
		}
		accepted, err := acceptedScheduleSourceTx(ctx, s.db, repositoryID, defaultRef, entry.SourceOID)
		if err != nil {
			return nil, err
		}
		if !accepted {
			entry.Notes = append(entry.Notes, actions.Message{Code: "note.push_required", Path: entry.WorkflowPath, Detail: "This default-branch revision has no retained accepted OwnGit push. Push the default branch to OwnGit to run its schedules."})
		}
		entry.Paused, err = actionsSchedulePausedTx(ctx, s.db, entry)
		if err != nil {
			return nil, err
		}
		if entry.Paused {
			entry.PauseNote = "note.schedule_paused"
			entry.Notes = append(entry.Notes, actions.Message{Code: entry.PauseNote, Path: entry.WorkflowPath, Detail: "Schedule paused while its last run is unfinished or after an uncertain run. Rerun the workflow or run it now to resolve uncertain execution."})
		}
		views = append(views, entry)
	}
	return views, nil
}

func readActionsSchedules(ctx context.Context, queryer querier, repositoryID string) ([]ActionsSchedule, error) {
	rows, err := queryer.QueryContext(ctx, actionsScheduleSelect+` WHERE repository_id=? ORDER BY workflow_path,cron`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var schedules []ActionsSchedule
	for rows.Next() {
		var entry ActionsSchedule
		var next, updated int64
		var slot, admitted sql.NullInt64
		if err := rows.Scan(&entry.RepositoryID, &entry.WorkflowPath, &entry.Cron, &entry.SourceOID, &next, &slot, &admitted, &entry.LastRunID, &updated); err != nil {
			return nil, err
		}
		entry.NextDueAt, entry.UpdatedAt = time.Unix(next, 0).UTC(), time.Unix(updated, 0).UTC()
		entry.LastSlotAt, entry.LastAdmittedAt = nullableTimePointer(slot), nullableTimePointer(admitted)
		schedules = append(schedules, entry)
	}
	return schedules, rows.Err()
}

func acceptedScheduleSourceTx(ctx context.Context, queryer querier, repositoryID, ref, oid string) (bool, error) {
	var accepted bool
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM actions_accepted_pushes WHERE repository_id=? AND ref_name=? AND new_oid=?)`, repositoryID, ref, oid).Scan(&accepted)
	return accepted, err
}

func actionsSchedulePausedTx(ctx context.Context, queryer querier, entry ActionsSchedule) (bool, error) {
	var paused bool
	err := queryer.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM check_jobs WHERE run_id=? AND run_id!='' AND status IN ('waiting','pending','claimed','started')) OR
		EXISTS(SELECT 1 FROM actions_runs r JOIN check_jobs j ON j.run_id=r.id
		 WHERE r.repository_id=? AND r.workflow_path=? AND j.status='ambiguous'
		 AND NOT EXISTS(SELECT 1 FROM actions_runs later WHERE later.repository_id=r.repository_id AND later.workflow_path=r.workflow_path AND later.number>r.number))`, entry.LastRunID, entry.RepositoryID, entry.WorkflowPath).Scan(&paused)
	return paused, err
}
