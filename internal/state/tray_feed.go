package state

import (
	"context"
	"fmt"
	"time"
)

// FeedRecord is a record the tray's event feed reports besides pushes: a
// pull request that was opened, or an automatic check, an import or a
// backup that did not finish well. Each comes from a record OwnGit keeps
// anyway; the feed reads them by the time they were written.
type FeedRecord struct {
	// Kind is NotifyPullRequest, NotifyCheckFailed, NotifyImportFailed or
	// NotifyBackupFailed.
	Kind string
	// ID names the record among those of its kind: the pull request's
	// repository and number, or the check job's, import run's or backup
	// run's ID.
	ID string
	// RepositoryID and RepositoryName are "" for a backup. RepositoryName
	// is RepositoryID when the repository is gone.
	RepositoryID, RepositoryName string
	// Number is the pull request's number, or the number of the pull request
	// a check ran for, or 0.
	Number int
	// Title is the pull request's title.
	Title string
	// Branch is the pull request's source branch, or the branch or ref a
	// check ran for.
	Branch string
	// Message says what went wrong, as the record keeps it.
	Message string
	// At is when OwnGit wrote the record: the pull request opened, or the
	// check, import or backup ended.
	At time.Time
}

// feedQueries select, for each kind, the records written after the first
// and up to the second argument, the oldest first; the time is the last
// column. Times are Unix seconds except for check jobs, which keep
// nanoseconds.
var feedQueries = map[string]string{
	NotifyPullRequest: `SELECT p.repository_id || '/' || p.number, p.repository_id, r.name, p.number, p.title, p.source_branch, '', p.created_at
		FROM pull_requests p JOIN repositories r ON r.id = p.repository_id
		WHERE p.status != 'creating' AND p.created_at > ? AND p.created_at <= ? ORDER BY p.created_at, p.repository_id, p.number`,
	NotifyCheckFailed: `SELECT j.id, j.repository_id, r.name, j.pull_request_number, '', j.trigger_ref, j.summary, j.finished_at / 1000000000
		FROM check_jobs j JOIN repositories r ON r.id = j.repository_id
		WHERE j.status IN ('failed','error') AND j.finished_at > ? * 1000000000 AND j.finished_at <= ? * 1000000000 ORDER BY j.finished_at, j.id`,
	NotifyImportFailed: `SELECT i.id, i.repository_id, COALESCE(r.name, i.repository_id), 0, '', '', i.message, i.finished_at
		FROM import_runs i LEFT JOIN repositories r ON r.id = i.repository_id
		WHERE i.status IN ('failed','interrupted','unresolved') AND i.finished_at > ? AND i.finished_at <= ? ORDER BY i.finished_at, i.id`,
	NotifyBackupFailed: `SELECT b.id, '', '', 0, '', '', b.message, COALESCE(b.finished_at, b.started_at)
		FROM backup_runs b
		WHERE b.status IN ('failed','interrupted') AND COALESCE(b.finished_at, b.started_at) > ? AND COALESCE(b.finished_at, b.started_at) <= ? ORDER BY 8, b.id`,
}

// FeedRecords returns up to limit records of kind written after after and
// up to until, both whole seconds, the oldest first, and how many there
// are in all.
func (s *Store) FeedRecords(ctx context.Context, kind string, after, until time.Time, limit int) ([]FeedRecord, int, error) {
	query, known := feedQueries[kind]
	if !known {
		return nil, 0, fmt.Errorf("the event feed has no records of kind %q", kind)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+query+`)`, after.Unix(), until.Unix()).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, query+` LIMIT ?`, after.Unix(), until.Unix(), limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	records := []FeedRecord{}
	for rows.Next() {
		record := FeedRecord{Kind: kind}
		var at int64
		if err := rows.Scan(&record.ID, &record.RepositoryID, &record.RepositoryName, &record.Number, &record.Title, &record.Branch, &record.Message, &at); err != nil {
			return nil, 0, err
		}
		record.At = time.Unix(at, 0)
		records = append(records, record)
	}
	return records, total, rows.Err()
}
