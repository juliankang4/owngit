package state

import (
	"context"
	"errors"
)

// LatestCheckAttemptForPullRequestHistory returns the newest attempt for any
// source revision recorded for one pull request. Evidence for an unrelated
// branch is never returned, so a pull request without its own evidence reads
// as absent rather than as an earlier result for this change. The subquery is
// bounded by the pull request's recorded revisions and the lookup uses the
// check_attempts_revision index.
func (s *Store) LatestCheckAttemptForPullRequestHistory(ctx context.Context, repositoryID string, number int64) (CheckAttempt, bool, error) {
	return s.latestCheckAttempt(ctx, attemptSelect+` WHERE repository_id=? AND revision_oid IN (
		SELECT source_oid FROM pull_request_revisions WHERE repository_id=? AND pull_request_number=?
	) ORDER BY sequence DESC LIMIT 1`, repositoryID, repositoryID, number)
}

// RecentCheckAttemptsForTask returns at most limit attempts of one task,
// newest first, with their results, and reports whether older attempts exist.
// A display reads only what it shows, however long the task's history grows.
func (s *Store) RecentCheckAttemptsForTask(ctx context.Context, repositoryID, taskID string, limit int) ([]CheckAttempt, bool, error) {
	if limit < 1 || limit > 1000 {
		return nil, false, errors.New("invalid check attempt limit")
	}
	rows, err := s.db.QueryContext(ctx, attemptSelect+` WHERE repository_id=? AND task_id=? ORDER BY sequence DESC LIMIT ?`, repositoryID, taskID, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var attempts []CheckAttempt
	for rows.Next() {
		attempt, err := scanCheckAttempt(rows)
		if err != nil {
			return nil, false, err
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	more := len(attempts) > limit
	if more {
		attempts = attempts[:limit]
	}
	for index := range attempts {
		if err := loadCheckResults(ctx, s.db, &attempts[index]); err != nil {
			return nil, false, err
		}
	}
	return attempts, more, nil
}
