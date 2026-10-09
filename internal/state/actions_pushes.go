package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"owngit/internal/actions"
)

const (
	MaximumAcceptedActionsPushes = 4096
	MaximumActionsPushRefBytes   = 4096
)

// AcceptedActionsPush is a branch update accepted by OwnGit's receive path.
// These machine-local records are not restored from a backup.
type AcceptedActionsPush struct {
	Sequence     int64
	RepositoryID string
	Ref          string
	OldOID       string
	NewOID       string
}

func ValidActionsPushRef(ref string) bool {
	return strings.HasPrefix(ref, "refs/heads/") && len(ref) <= MaximumActionsPushRefBytes && validImportRefName(ref)
}

func (s *Store) RecordAcceptedActionsPushes(ctx context.Context, repositoryID string, updates []AcceptedActionsPush, now time.Time) error {
	if repositoryID == "" || now.IsZero() {
		return ErrInvalidActionsRun
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions_accepted_pushes WHERE repository_id=?`, repositoryID).Scan(&count); err != nil {
		return err
	}
	for _, update := range updates {
		if update.NewOID == "" || !strings.HasPrefix(update.Ref, "refs/heads/") {
			continue
		}
		if !validObjectID(update.NewOID) || update.OldOID != "" && !validObjectID(update.OldOID) {
			return ErrInvalidActionsRun
		}
		if count >= MaximumAcceptedActionsPushes {
			result, err := tx.ExecContext(ctx, `DELETE FROM actions_accepted_pushes WHERE sequence IN (SELECT sequence FROM actions_accepted_pushes WHERE repository_id=? AND consumed=1 ORDER BY sequence LIMIT 1)`, repositoryID)
			if err != nil {
				return err
			}
			deleted, err := result.RowsAffected()
			if err != nil {
				return err
			}
			count -= int(deleted)
		}
		if count >= MaximumAcceptedActionsPushes || !ValidActionsPushRef(update.Ref) {
			if err := refuseAcceptedActionsPushTx(ctx, tx, repositoryID, update, now); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO actions_accepted_pushes(repository_id,ref_name,old_oid,new_oid) VALUES(?,?,?,?)`, repositoryID, update.Ref, update.OldOID, update.NewOID); err != nil {
			return err
		}
		count++
	}
	return tx.Commit()
}

func refuseAcceptedActionsPushTx(ctx context.Context, tx *sql.Tx, repositoryID string, update AcceptedActionsPush, now time.Time) error {
	policy, exists, err := readCheckPolicyTx(ctx, tx, repositoryID)
	if err != nil {
		return err
	}
	if !exists || !policy.RunWorkflows || !checkConsentCurrent(policy) || !policyAllowsCheckEvent(policy, "push") {
		return nil
	}
	branch := strings.TrimPrefix(update.Ref, "refs/heads/")
	original := branch
	if len(branch) > MaximumCheckTriggerRefBytes {
		branch = fmt.Sprintf("%s%x", ActionsRefusedRefPrefix, sha256.Sum256([]byte(branch)))
	}
	original = strings.ToValidUTF8(original, "�")
	if len(original) > 4096 {
		original = strings.ToValidUTF8(original[:4093], "�") + "..."
	}
	note := actions.Message{Code: "workflow.limit", Path: original, Detail: fmt.Sprintf("The accepted push queue holds %d unconsumed updates. This push has no workflow authority. Push the branch again after the queue drains. Original branch: %q", MaximumAcceptedActionsPushes, original), Args: map[string]string{"what": "Accepted push queue", "limit": fmt.Sprint(MaximumAcceptedActionsPushes)}}
	refDigest := sha256.Sum256([]byte(update.Ref))
	request := ActionsRunRequest{Run: ActionsRun{RepositoryID: repositoryID, WorkflowPath: fmt.Sprintf("%s%x", ActionsRefusedWorkflowPrefix, sha256.Sum256([]byte("accepted-push-limit"))), Event: "push", EventKey: fmt.Sprintf("push/%x/%s", refDigest[:16], update.NewOID), SourceOID: update.NewOID, TriggerRef: branch, Outcome: actions.StatusRefused, Reason: note.Detail, Facts: actions.RunFacts{Notes: []actions.Message{note}}}}
	_, _, err = admitActionsRunTx(ctx, tx, request, now)
	return err
}

func (s *Store) PendingAcceptedActionsPushes(ctx context.Context, repositoryID string, limit int) ([]AcceptedActionsPush, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,repository_id,ref_name,old_oid,new_oid FROM (SELECT sequence,repository_id,ref_name,old_oid,new_oid,ROW_NUMBER() OVER (PARTITION BY repository_id ORDER BY sequence) AS position FROM actions_accepted_pushes WHERE consumed=0 AND (?='' OR repository_id=?)) ORDER BY position,sequence LIMIT ?`, repositoryID, repositoryID, min(max(limit, 1), MaximumAcceptedActionsPushes))
	if err != nil {
		return nil, err
	}
	var pushes []AcceptedActionsPush
	for rows.Next() {
		var push AcceptedActionsPush
		if err := rows.Scan(&push.Sequence, &push.RepositoryID, &push.Ref, &push.OldOID, &push.NewOID); err != nil {
			rows.Close()
			return nil, err
		}
		pushes = append(pushes, push)
	}
	return pushes, closeRows(rows)
}

func (s *Store) ConsumeAcceptedActionsPush(ctx context.Context, sequence int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE actions_accepted_pushes SET consumed=1 WHERE sequence=?`, sequence)
	return err
}

func acceptedActionsPushTx(ctx context.Context, queryer querier, repositoryID string, sequence int64) (AcceptedActionsPush, bool, error) {
	var push AcceptedActionsPush
	err := queryer.QueryRowContext(ctx, `SELECT sequence,repository_id,ref_name,old_oid,new_oid FROM actions_accepted_pushes WHERE repository_id=? AND sequence=? AND consumed=0`, repositoryID, sequence).Scan(&push.Sequence, &push.RepositoryID, &push.Ref, &push.OldOID, &push.NewOID)
	if err == sql.ErrNoRows {
		return push, false, nil
	}
	return push, err == nil, err
}

func (s *Store) AcceptedActionsPushBySequence(ctx context.Context, repositoryID string, sequence int64) (AcceptedActionsPush, bool, error) {
	return acceptedActionsPushTx(ctx, s.db, repositoryID, sequence)
}

func (s *Store) AcceptedActionsPush(ctx context.Context, repositoryID, ref, oid string) (AcceptedActionsPush, bool, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT sequence FROM actions_accepted_pushes WHERE repository_id=? AND ref_name=? AND new_oid=? AND consumed=0 ORDER BY sequence LIMIT 1`, repositoryID, ref, oid).Scan(&sequence)
	if err == sql.ErrNoRows {
		return AcceptedActionsPush{}, false, nil
	}
	if err != nil {
		return AcceptedActionsPush{}, false, err
	}
	return acceptedActionsPushTx(ctx, s.db, repositoryID, sequence)
}

func acceptedActionsSourceTx(ctx context.Context, queryer querier, repositoryID, oid string) (bool, error) {
	var accepted bool
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM actions_accepted_pushes WHERE repository_id=? AND new_oid=?)`, repositoryID, oid).Scan(&accepted)
	return accepted, err
}

// ActionsPullRequestAdmissionNote explains missing source authority to readers.
func (s *Store) ActionsPullRequestAdmissionNote(ctx context.Context, repositoryID, sourceOID string) (*actions.Message, error) {
	accepted, err := acceptedActionsSourceTx(ctx, s.db, repositoryID, sourceOID)
	if err != nil || accepted {
		return nil, err
	}
	return &actions.Message{Code: "note.push_required", Detail: "This source revision has no accepted OwnGit push. Push the source branch to OwnGit to run its workflows."}, nil
}
