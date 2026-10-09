package state

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"owngit/internal/checkworkflow"
)

const jsonAdmissionRefusalCredentialID = "json-admission-refusal"

const jsonAdmissionRefusalLookup = `SELECT credential_id FROM check_attempts WHERE repository_id=? AND task_id=? AND log_error=?`
const jsonAdmissionRefusalLogError = "No raw log: the checks were not admitted. Event "

func jsonAdmissionRefusalEventKey(eventKey string) string {
	return jsonAdmissionRefusalLogError + digestFields(eventKey)
}

func isJSONAdmissionRefusal(credentialID string) bool {
	return credentialID == jsonAdmissionRefusalCredentialID
}

// JSONAdmissionRefusal identifies a selected event that could not start its
// configured checks. Request carries its revision and trigger, not a job.
type JSONAdmissionRefusal struct {
	Request CheckJobRequest
	Reason  string
}

func (s *Store) recordJSONAdmissionRefusalTx(ctx context.Context, tx *sql.Tx, refusal JSONAdmissionRefusal, now time.Time) error {
	request := refusal.Request
	if (request.Trigger != checkworkflow.EventPush && request.Trigger != checkworkflow.EventPullRequest) ||
		!validObjectID(request.SourceOID) || request.EventKey == "" || len(request.EventKey) > MaximumCheckEventKeyBytes ||
		request.TriggerRef == "" || len(request.TriggerRef) > MaximumCheckTriggerRefBytes {
		return ErrInvalidCheckJob
	}
	taskID := automaticCheckTaskID(request)
	logError := jsonAdmissionRefusalEventKey(request.EventKey)
	rows, err := tx.QueryContext(ctx, jsonAdmissionRefusalLookup,
		request.RepositoryID, taskID, logError)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var credentialID string
		if err := rows.Scan(&credentialID); err != nil {
			rows.Close()
			return err
		}
		found = found || isJSONAdmissionRefusal(credentialID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	if err := ensureAutomaticCheckTaskTx(ctx, tx, request.RepositoryID, taskID, "Automatic checks", now); err != nil {
		return err
	}
	var attemptID string
	for {
		attemptID, err = RandomID()
		if err != nil {
			return err
		}
		_, exists, err := readAttemptTx(ctx, tx, attemptID)
		if err != nil {
			return err
		}
		if !exists {
			break
		}
	}
	reason := boundedJSONAdmissionReason(refusal.Reason)
	check := CheckDefinition{Name: "Admission", Command: "Not run"}
	result := CheckResult{Name: check.Name, Command: check.Command, Status: AttemptUnavailable, OutputExcerpt: reason}
	attempt, err := s.registerCheckAttemptTx(ctx, tx, CheckAttempt{
		ID: attemptID, TaskID: taskID, RepositoryID: request.RepositoryID,
		RevisionOID: request.SourceOID, WorktreeState: WorktreeUnknown,
		StartedAt: now.UTC(), CreatedAt: now.UTC(),
		Protection: ProtectionUnknown, ExecutionScope: ExecutionScopeInherited,
		CredentialID: jsonAdmissionRefusalCredentialID, Checks: []CheckDefinition{check},
	})
	if err != nil {
		return err
	}
	attempt.SubmittedWorktreeState = WorktreeUnknown
	attempt.FinishedAt = now.UTC()
	attempt.SubmittedLogDigest = digestFields("")
	attempt.CompletionDigest = completionDigest(attempt, []CheckResult{result})
	status, summary := checkAttemptOutcome([]CheckResult{result}, false, WorktreeUnknown, false, attempt.CredentialID)
	if _, err := tx.ExecContext(ctx, `UPDATE check_attempts SET status=?,finished_at=?,summary=?,submitted_worktree_state=?,completion_digest=?,submitted_log_digest=?,log_error=? WHERE id=?`,
		status, attempt.FinishedAt.UnixNano(), summary, attempt.SubmittedWorktreeState, attempt.CompletionDigest, attempt.SubmittedLogDigest,
		logError, attempt.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO check_results(attempt_id,position,name,command,status,exit_code,duration_ms,output_excerpt,truncated,cleanup_error,role) VALUES(?,0,?,?,?,NULL,0,?,0,'','')`,
		attempt.ID, result.Name, result.Command, result.Status, result.OutputExcerpt)
	return err
}

func boundedJSONAdmissionReason(reason string) string {
	reason = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(reason, " "))
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		reason = "Configured checks could not be admitted."
	}
	if len(reason) > 400 {
		reason = reason[:400]
		for !utf8.ValidString(reason) {
			reason = reason[:len(reason)-1]
		}
	}
	return reason
}
