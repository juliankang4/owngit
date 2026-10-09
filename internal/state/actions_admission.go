package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"owngit/internal/actions"
)

func (s *Store) ActionsPullRequestAction(ctx context.Context, repositoryID string, number int64, source, target string) (string, error) {
	var firstSource, firstTarget string
	err := s.db.QueryRowContext(ctx, `SELECT source_oid,target_oid FROM pull_request_revisions WHERE repository_id=? AND pull_request_number=? ORDER BY rowid LIMIT 1`, repositoryID, number).Scan(&firstSource, &firstTarget)
	if errors.Is(err, sql.ErrNoRows) {
		return "opened", nil
	}
	if err != nil {
		return "", err
	}
	if firstSource == source && firstTarget == target {
		return "opened", nil
	}
	return "synchronize", nil
}

type CheckEventAdmission struct {
	Job           *CheckJob
	Runs          []ActionsRun
	Refusals      []actions.Message
	Admitted      bool
	JSONQueueFull bool
}

// AdmitCheckEvent gives JSON checks the first queue slot, then admits each file
// whole in caller-supplied path order, under one policy generation and transaction.
func (s *Store) AdmitCheckEvent(ctx context.Context, repositoryID string, expected ExpectedCheckPolicy, jsonJob *CheckJobRequest, runs []ActionsRunRequest, now time.Time, acceptedPushSequence ...int64) (CheckEventAdmission, error) {
	if now.IsZero() || len(acceptedPushSequence) > 1 {
		return CheckEventAdmission{}, ErrInvalidActionsRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CheckEventAdmission{}, err
	}
	defer tx.Rollback()
	policy, exists, err := readCheckPolicyTx(ctx, tx, repositoryID)
	if err != nil {
		return CheckEventAdmission{}, err
	}
	if !exists {
		return CheckEventAdmission{}, ErrCheckPolicyMissing
	}
	if policy.Version != expected.Version || policy.Digest != expected.Digest {
		return CheckEventAdmission{}, ErrCheckPolicyStale
	}
	var sequence int64
	if len(acceptedPushSequence) != 0 {
		sequence = acceptedPushSequence[0]
	}
	acceptedPush, accepted, err := acceptedActionsPushTx(ctx, tx, repositoryID, sequence)
	if err != nil {
		return CheckEventAdmission{}, err
	}
	result := CheckEventAdmission{}
	if jsonJob != nil {
		if jsonJob.RepositoryID != repositoryID || jsonJob.RunID != "" {
			return result, ErrInvalidCheckJob
		}
		if err := validateCheckJobRequest(*jsonJob); err != nil {
			return result, err
		}
		job, deduped, err := admitCheckJobTx(ctx, tx, *jsonJob, now)
		if errors.Is(err, ErrCheckQueueFull) {
			result.JSONQueueFull = true
		} else if err != nil {
			return result, err
		} else {
			result.Job, result.Admitted = &job, !deduped
		}
	}
	for _, request := range runs {
		if request.Run.RepositoryID != repositoryID {
			return CheckEventAdmission{}, ErrInvalidActionsRun
		}
		existing, found, err := readActionsRunTx(ctx, tx, actionsRunSelect+` WHERE repository_id=? AND event=? AND event_key=? AND workflow_path=? AND rerun_generation=?`, repositoryID, request.Run.Event, request.Run.EventKey, request.Run.WorkflowPath, request.Run.RerunGeneration)
		if err != nil {
			return CheckEventAdmission{}, err
		}
		if found {
			result.Runs = append(result.Runs, existing)
			continue
		}
		switch request.Run.Event {
		case "push":
			if !accepted || request.Run.SourceOID != acceptedPush.NewOID || request.Run.TriggerRef != strings.TrimPrefix(acceptedPush.Ref, "refs/heads/") && !validRefusedActionsIdentity(request.Run.TriggerRef, ActionsRefusedRefPrefix) {
				continue
			}
		case ActionsEventSchedule:
			authorized, err := acceptedScheduleSourceTx(ctx, tx, repositoryID, "refs/heads/"+request.Run.TriggerRef, request.Run.SourceOID)
			if err != nil {
				return CheckEventAdmission{}, err
			}
			if !authorized {
				continue
			}
		case "pull_request":
			authorized, err := acceptedActionsSourceTx(ctx, tx, repositoryID, request.Run.SourceOID)
			if err != nil {
				return CheckEventAdmission{}, err
			}
			if !authorized {
				continue
			}
		}
		run, deduped, refusal, err := admitActionsEventRunTx(ctx, tx, request, now)
		if err != nil {
			return CheckEventAdmission{}, err
		}
		if refusal != nil {
			result.Refusals = append(result.Refusals, *refusal)
			continue
		}
		result.Runs = append(result.Runs, run)
		result.Admitted = result.Admitted || !deduped
	}
	if err := settleActionsRunsTx(ctx, tx, now, ""); err != nil {
		return CheckEventAdmission{}, err
	}
	if accepted {
		if _, err := tx.ExecContext(ctx, `UPDATE actions_accepted_pushes SET consumed=1 WHERE sequence=?`, sequence); err != nil {
			return CheckEventAdmission{}, err
		}
	}
	return result, tx.Commit()
}

func admitActionsEventRunTx(ctx context.Context, tx *sql.Tx, request ActionsRunRequest, now time.Time) (ActionsRun, bool, *actions.Message, error) {
	if _, err := tx.ExecContext(ctx, `SAVEPOINT actions_event_run`); err != nil {
		return ActionsRun{}, false, nil, err
	}
	run, deduped, err := admitActionsRunTx(ctx, tx, request, now)
	if errors.Is(err, ErrInvalidActionsRun) || errors.Is(err, ErrInvalidCheckJob) {
		if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO actions_event_run`); rollbackErr != nil {
			return ActionsRun{}, false, nil, rollbackErr
		}
		note := actions.Message{Code: "workflow.invalid", Path: boundedActionsRefusalText(request.Run.WorkflowPath), Detail: boundedActionsRefusalText(err.Error())}
		refused := request.Run
		refused.Outcome, refused.Reason = actions.StatusRefused, note.Detail
		refused.ConcurrencyGroup, refused.ConcurrencyQueue, refused.CancelInProgress = "", "single", false
		refused.InputsJSON = "{}"
		refused.Facts = actions.RunFacts{PullRequestAction: request.Run.Facts.PullRequestAction, PullRequestHeadRef: request.Run.Facts.PullRequestHeadRef, Notes: []actions.Message{note}}
		for _, original := range request.Run.Facts.Notes {
			if original.Code == "workflow.limit" && original.Path != "" && len(original.Path) <= 4096 {
				refused.Facts.Notes = append(refused.Facts.Notes, original)
				break
			}
		}
		run, deduped, err = admitActionsRunTx(ctx, tx, ActionsRunRequest{Run: refused}, now)
		if errors.Is(err, ErrInvalidActionsRun) || errors.Is(err, ErrInvalidCheckJob) {
			if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO actions_event_run`); rollbackErr != nil {
				return ActionsRun{}, false, nil, rollbackErr
			}
			if _, releaseErr := tx.ExecContext(ctx, `RELEASE actions_event_run`); releaseErr != nil {
				return ActionsRun{}, false, nil, releaseErr
			}
			return ActionsRun{}, false, &note, nil
		}
	}
	if err != nil {
		return ActionsRun{}, false, nil, err
	}
	_, err = tx.ExecContext(ctx, `RELEASE actions_event_run`)
	return run, deduped, nil, err
}

func boundedActionsRefusalText(value string) string {
	value = strconv.QuoteToASCII(value)
	if len(value) > 4096 {
		value = value[:4080] + " [cut]"
	}
	return value
}

func (s *Store) RerunActionsRun(ctx context.Context, originalID string, request ActionsRunRequest, now time.Time) (ActionsRun, bool, error) {
	if !validAttemptID(originalID) || now.IsZero() {
		return ActionsRun{}, false, ErrInvalidActionsRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ActionsRun{}, false, err
	}
	defer tx.Rollback()
	original, exists, err := readActionsRunTx(ctx, tx, actionsRunSelect+` WHERE repository_id=? AND id=?`, request.Run.RepositoryID, originalID)
	if err != nil {
		return ActionsRun{}, false, err
	}
	if !exists {
		return ActionsRun{}, false, ErrInvalidActionsRun
	}
	rootID := original.RerunRoot
	if rootID == "" {
		rootID = original.ID
	}
	root, _, err := readActionsRunTx(ctx, tx, actionsRunSelect+` WHERE id=?`, rootID)
	if err != nil {
		return ActionsRun{}, false, err
	}
	existing, exists, err := unfinishedActionsRerunTx(ctx, tx, rootID)
	if err != nil {
		return ActionsRun{}, false, err
	}
	if exists {
		return existing, true, tx.Commit()
	}
	request.Run.ID, request.Run.Number = "", 0
	request.Run.CancelRequestedAt = nil
	if request.Run.InputsJSON == "" {
		request.Run.InputsJSON = `{}`
	}
	request.Run.RerunRoot = root.ID
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rerun_generation),0)+1 FROM actions_runs WHERE id=? OR rerun_root=?`, root.ID, root.ID).Scan(&request.Run.RerunGeneration); err != nil {
		return ActionsRun{}, false, err
	}
	if !sameActionsRunRoot(request.Run, root) {
		return ActionsRun{}, false, fmt.Errorf("%w: rerun event differs from its root", ErrInvalidActionsRun)
	}
	run, deduped, err := admitActionsRunTx(ctx, tx, request, now)
	if err != nil {
		return ActionsRun{}, false, err
	}
	if err := settleActionsRunsTx(ctx, tx, now, ""); err != nil {
		return ActionsRun{}, false, err
	}
	return run, deduped, tx.Commit()
}

func unfinishedActionsRerunTx(ctx context.Context, queryer querier, root string) (ActionsRun, bool, error) {
	return readActionsRunTx(ctx, queryer, actionsRunSelect+` WHERE rerun_root=? AND EXISTS (SELECT 1 FROM check_jobs j WHERE j.run_id=actions_runs.id AND j.status IN ('pending','waiting','claimed','started')) ORDER BY rerun_generation DESC LIMIT 1`, root)
}

func (s *Store) UnfinishedActionsRerun(ctx context.Context, repositoryID, originalID string) (ActionsRun, bool, error) {
	original, exists, err := s.ActionsRun(ctx, repositoryID, originalID)
	if err != nil || !exists {
		return ActionsRun{}, false, err
	}
	root := original.RerunRoot
	if root == "" {
		root = original.ID
	}
	return unfinishedActionsRerunTx(ctx, s.db, root)
}
