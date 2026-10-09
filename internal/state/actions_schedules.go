package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"owngit/internal/actions"
)

const MaximumActionsSchedules = 32
const ActionsScheduleSpacing = 5 * time.Minute

func schedulePolicyActive(policy CheckPolicy) bool {
	return policy.RunWorkflows && checkConsentCurrent(policy) && !policy.Execution.Legacy && policyAllowsCheckEvent(policy, ActionsEventSchedule)
}

// AcceptedActionsScheduleSource includes consumed receive records. Imports and
// restored heads without a retained accepted push cannot supply workflow code.
func (s *Store) AcceptedActionsScheduleSource(ctx context.Context, repositoryID, ref, oid string) (bool, error) {
	return acceptedScheduleSourceTx(ctx, s.db, repositoryID, ref, oid)
}

// RebuildActionsSchedules keeps unchanged entries' timing. Callers supply the
// current default branch, bounded file order and current policy generation.
func (s *Store) RebuildActionsSchedules(ctx context.Context, repositoryID, ref, oid string, expected ExpectedCheckPolicy, entries []ActionsSchedule, now time.Time) ([]actions.Message, error) {
	if now.IsZero() || len(entries) != 0 && (!ValidActionsPushRef(ref) || !validObjectID(oid)) {
		return nil, ErrInvalidActionsRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	policy, exists, err := readCheckPolicyTx(ctx, tx, repositoryID)
	if err != nil {
		return nil, err
	}
	if !exists || policy.Version != expected.Version || policy.Digest != expected.Digest {
		return nil, ErrCheckPolicyStale
	}
	if !schedulePolicyActive(policy) {
		entries = nil
	}
	old, err := readActionsSchedules(ctx, tx, repositoryID)
	if err != nil {
		return nil, err
	}
	retained := make(map[string]time.Time, len(old))
	for _, entry := range old {
		retained[entry.WorkflowPath+"\x00"+entry.Cron] = entry.NextDueAt
	}
	kept := map[string]bool{}
	var notes []actions.Message
	for _, entry := range entries {
		key := entry.WorkflowPath + "\x00" + entry.Cron
		if kept[key] {
			continue
		}
		if !validActionsWorkflowPath(entry.WorkflowPath) {
			return nil, ErrInvalidActionsRun
		}
		cron, err := actions.ParseCron(entry.Cron)
		if err != nil {
			return nil, err
		}
		next, exists := retained[key]
		if !exists {
			next, err = cron.Next(now)
			if err != nil {
				notes = append(notes, err.(*actions.Refusal).Message)
				continue
			}
		}
		if len(kept) >= MaximumActionsSchedules {
			notes = append(notes, actions.Message{Code: "workflow.limit", Path: entry.WorkflowPath, Detail: "Schedules per repository exceed OwnGit's limit of 32.", Args: map[string]string{"what": "Schedules per repository", "limit": "32"}})
			continue
		}
		kept[key] = true
		_, err = tx.ExecContext(ctx, `INSERT INTO actions_schedules(repository_id,workflow_path,cron,source_oid,next_due_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(repository_id,workflow_path,cron) DO UPDATE SET source_oid=excluded.source_oid,updated_at=excluded.updated_at`, repositoryID, entry.WorkflowPath, entry.Cron, oid, next.Unix(), now.Unix())
		if err != nil {
			return nil, err
		}
	}
	for _, entry := range old {
		if !kept[entry.WorkflowPath+"\x00"+entry.Cron] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM actions_schedules WHERE repository_id=? AND workflow_path=? AND cron=?`, repositoryID, entry.WorkflowPath, entry.Cron); err != nil {
				return nil, err
			}
		}
	}
	return notes, tx.Commit()
}

func scheduleOftenNote() actions.Message {
	return actions.Message{Code: "note.schedule_often", Detail: "This cron requests runs more often than every five minutes. OwnGit spaces schedule admissions at least five minutes apart."}
}

func ActionsScheduleEventKey(entry ActionsSchedule, slot time.Time) string {
	digest := sha256.Sum256([]byte(entry.WorkflowPath + "\x00" + entry.Cron))
	return fmt.Sprintf("schedule/%x/%d", digest[:16], slot.Unix())
}

// AdmitScheduledActionsRun claims the observed due time and admits the planned
// run in one transaction. A stale observation or pause consumes no slot.
func (s *Store) AdmitScheduledActionsRun(ctx context.Context, entry ActionsSchedule, defaultRef string, expected ExpectedCheckPolicy, request ActionsRunRequest, now time.Time) (CheckEventAdmission, error) {
	if now.IsZero() || !ValidActionsPushRef(defaultRef) {
		return CheckEventAdmission{}, ErrInvalidActionsRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckEventAdmission{}, err
	}
	defer tx.Rollback()
	policy, exists, err := readCheckPolicyTx(ctx, tx, entry.RepositoryID)
	if err != nil {
		return CheckEventAdmission{}, err
	}
	if !exists || policy.Version != expected.Version || policy.Digest != expected.Digest {
		return CheckEventAdmission{}, ErrCheckPolicyStale
	}
	if !schedulePolicyActive(policy) {
		return CheckEventAdmission{}, ErrCheckConsentRequired
	}
	entry, found, err := lookupScheduledActionsClaimTx(ctx, tx, entry)
	if err != nil {
		return CheckEventAdmission{}, err
	}
	if !found || entry.NextDueAt.After(now) {
		return CheckEventAdmission{}, nil
	}
	paused, err := actionsSchedulePausedTx(ctx, tx, entry)
	if err != nil || paused {
		return CheckEventAdmission{}, err
	}
	accepted, err := acceptedScheduleSourceTx(ctx, tx, entry.RepositoryID, defaultRef, entry.SourceOID)
	if err != nil || !accepted {
		return CheckEventAdmission{}, err
	}
	cron, err := actions.ParseCron(entry.Cron)
	if err != nil {
		return CheckEventAdmission{}, err
	}
	slot, err := cron.Latest(now)
	if err != nil {
		return CheckEventAdmission{}, err
	}
	if slot.Before(entry.NextDueAt) {
		return CheckEventAdmission{}, ErrInvalidActionsRun
	}
	next, err := cron.Next(now.Add(ActionsScheduleSpacing).Add(-time.Nanosecond))
	var horizon *actions.Refusal
	if err != nil && (!errors.As(err, &horizon) || horizon.Message.Code != "workflow.cron_never") {
		return CheckEventAdmission{}, err
	}
	if entry.LastAdmittedAt != nil && now.Before(entry.LastAdmittedAt.Add(ActionsScheduleSpacing)) {
		return CheckEventAdmission{}, nil
	}
	run := &request.Run
	if run.RepositoryID != entry.RepositoryID || run.WorkflowPath != entry.WorkflowPath || run.SourceOID != entry.SourceOID || run.Event != ActionsEventSchedule {
		return CheckEventAdmission{}, ErrInvalidActionsRun
	}
	run.ScheduledFor, run.EventKey = &slot, ActionsScheduleEventKey(entry, slot)
	if horizon != nil {
		run.Facts.Notes = append(run.Facts.Notes, horizon.Message)
	}
	if slot.After(entry.NextDueAt) {
		run.Facts.Notes = append(run.Facts.Notes, actions.Message{Code: "note.missed", Detail: fmt.Sprintf("Missed schedule times collapsed into the latest slot %s, admitted at %s.", slot.Format(time.RFC3339), now.UTC().Format(time.RFC3339)), Args: map[string]string{"slot": slot.Format(time.RFC3339), "time": now.UTC().Format(time.RFC3339)}})
	}
	if cron.Often(now) {
		run.Facts.Notes = append(run.Facts.Notes, scheduleOftenNote())
	}
	// The conditional due-time mutation is the claim; a later run write failure
	// rolls it back together with jobs and local plans.
	key := []any{entry.RepositoryID, entry.WorkflowPath, entry.Cron, entry.SourceOID, entry.NextDueAt.Unix()}
	query := `DELETE FROM actions_schedules WHERE repository_id=? AND workflow_path=? AND cron=? AND source_oid=? AND next_due_at=?`
	args := key
	if horizon == nil {
		query = `UPDATE actions_schedules SET next_due_at=?,last_slot_at=?,last_admitted_at=?,updated_at=? WHERE repository_id=? AND workflow_path=? AND cron=? AND source_oid=? AND next_due_at=?`
		args = append([]any{next.Unix(), slot.Unix(), now.Unix(), now.Unix()}, key...)
	}
	claimed, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return CheckEventAdmission{}, err
	}
	count, err := claimed.RowsAffected()
	if err != nil || count != 1 {
		return CheckEventAdmission{}, err
	}
	admitted, deduped, refusal, err := admitActionsEventRunTx(ctx, tx, request, now)
	if err != nil {
		return CheckEventAdmission{}, err
	}
	if refusal != nil {
		return CheckEventAdmission{}, ErrInvalidActionsRun
	}
	if horizon == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE actions_schedules SET last_run_id=? WHERE repository_id=? AND workflow_path=? AND cron=?`, admitted.ID, entry.RepositoryID, entry.WorkflowPath, entry.Cron); err != nil {
			return CheckEventAdmission{}, err
		}
	}
	return CheckEventAdmission{Runs: []ActionsRun{admitted}, Admitted: !deduped}, tx.Commit()
}

func lookupScheduledActionsClaimTx(ctx context.Context, queryer querier, observed ActionsSchedule) (ActionsSchedule, bool, error) {
	rows, err := readActionsSchedules(ctx, queryer, observed.RepositoryID)
	if err != nil {
		return ActionsSchedule{}, false, err
	}
	for _, row := range rows {
		if row.WorkflowPath == observed.WorkflowPath && row.Cron == observed.Cron && row.SourceOID == observed.SourceOID && row.NextDueAt.Equal(observed.NextDueAt) {
			return row, true, nil
		}
	}
	return ActionsSchedule{}, false, nil
}
