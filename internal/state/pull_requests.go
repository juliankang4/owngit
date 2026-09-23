package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PullRequestCreating = "creating"
	PullRequestOpen     = "open"
	PullRequestMerged   = "merged"

	ReviewPending          = "pending"
	ReviewApproved         = "approved"
	ReviewChangesRequested = "changes_requested"
	ReviewSkipped          = "skipped"
	// ReviewNotRequested is the default when creation omits a review choice.
	// It is not a waiting state: merge never requires a later decision.
	ReviewNotRequested = "not_requested"

	ReviewProvenanceRequest      = "review_request"
	ReviewProvenanceSkip         = "explicit_skip"
	ReviewProvenanceExternalTool = "supplied_external_tool"
	ReviewProvenanceDefault      = "default"

	MergeIntentPreparing = "preparing"
	MergeIntentPlanned   = "planned"
	MergeIntentReady     = "ready"
	MergeIntentComplete  = "complete"
)

type PullRequest struct {
	RepositoryID   string
	Number         int64
	Title          string
	SourceBranch   string
	TargetBranch   string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	MergeSourceOID string
	MergeTargetOID string
	MergeOID       string
	MergeReceipt   string
	MergedAt       *time.Time
}

type PullRequestRevision struct {
	RepositoryID      string
	PullRequestNumber int64
	SourceOID         string
	TargetOID         string
	RecordedAt        time.Time
}

type PullRequestReview struct {
	RepositoryID      string
	PullRequestNumber int64
	Sequence          int64
	SourceOID         string
	TargetOID         string
	Status            string
	ReviewerLabel     string
	Provenance        string
	// ReviewEventID is an optional identity for a pending review request, so a
	// retransmitted request is recognisable. Manual and default rows leave it
	// empty, and a stored value is still validated as a record fact.
	ReviewEventID string
	CreatedAt     time.Time
}

type PullRequestMergeIntent struct {
	RepositoryID      string
	PullRequestNumber int64
	SourceOID         string
	TargetOID         string
	Mode              string
	TreeOID           string
	ResultOID         string
	ReceiptRef        string
	Status            string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (s *Store) CreatePullRequest(ctx context.Context, repositoryID, title, sourceBranch, targetBranch, sourceOID, targetOID, initialReview string, now time.Time) (PullRequest, error) {
	record, err := s.BeginPullRequestCreation(ctx, repositoryID, title, sourceBranch, targetBranch, sourceOID, targetOID, initialReview, now)
	if err != nil {
		return PullRequest{}, err
	}
	return s.ActivatePullRequestCreation(ctx, repositoryID, record.Number, now)
}

func (s *Store) BeginPullRequestCreation(ctx context.Context, repositoryID, title, sourceBranch, targetBranch, sourceOID, targetOID, initialReview string, now time.Time) (PullRequest, error) {
	if initialReview != ReviewPending && initialReview != ReviewSkipped && initialReview != ReviewNotRequested {
		return PullRequest{}, errors.New("invalid initial review choice")
	}
	record := PullRequest{
		RepositoryID: repositoryID, Title: title, SourceBranch: sourceBranch, TargetBranch: targetBranch,
		Status: PullRequestCreating, CreatedAt: now, UpdatedAt: now,
	}
	revision := PullRequestRevision{RepositoryID: repositoryID, SourceOID: sourceOID, TargetOID: targetOID, RecordedAt: now}
	review := PullRequestReview{RepositoryID: repositoryID, SourceOID: sourceOID, TargetOID: targetOID, Status: initialReview, CreatedAt: now}
	switch initialReview {
	case ReviewPending:
		review.Provenance = ReviewProvenanceRequest
	case ReviewSkipped:
		review.Provenance = ReviewProvenanceSkip
	default:
		review.Provenance = ReviewProvenanceDefault
	}
	if err := validatePullRequestRecord(record); err != nil {
		return PullRequest{}, err
	}
	if !validObjectID(sourceOID) || !validObjectID(targetOID) {
		return PullRequest{}, errors.New("invalid pull request revision")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PullRequest{}, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM pull_requests WHERE repository_id=?`, repositoryID).Scan(&record.Number); err != nil {
		return PullRequest{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pull_requests(
		repository_id,number,title,source_branch,target_branch,status,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?)`, record.RepositoryID, record.Number, record.Title, record.SourceBranch, record.TargetBranch, record.Status, record.CreatedAt.Unix(), record.UpdatedAt.Unix()); err != nil {
		return PullRequest{}, err
	}
	revision.PullRequestNumber = record.Number
	if _, err := tx.ExecContext(ctx, `INSERT INTO pull_request_revisions(
		repository_id,pull_request_number,source_oid,target_oid,recorded_at
	) VALUES(?,?,?,?,?)`, revision.RepositoryID, revision.PullRequestNumber, revision.SourceOID, revision.TargetOID, revision.RecordedAt.Unix()); err != nil {
		return PullRequest{}, err
	}
	review.PullRequestNumber = record.Number
	review.Sequence = 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO pull_request_reviews(
		repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,review_event_id,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?)`, review.RepositoryID, review.PullRequestNumber, review.Sequence, review.SourceOID, review.TargetOID, review.Status, review.ReviewerLabel, review.Provenance, review.ReviewEventID, review.CreatedAt.Unix()); err != nil {
		return PullRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return PullRequest{}, err
	}
	return record, nil
}

func (s *Store) ActivatePullRequestCreation(ctx context.Context, repositoryID string, number int64, now time.Time) (PullRequest, error) {
	if now.IsZero() {
		return PullRequest{}, errors.New("pull request activation time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PullRequest{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE pull_requests SET status=?,updated_at=? WHERE repository_id=? AND number=? AND status=?`,
		PullRequestOpen, now.Unix(), repositoryID, number, PullRequestCreating)
	if err != nil {
		return PullRequest{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return PullRequest{}, err
		}
		return PullRequest{}, errors.New("provisional pull request was not found")
	}
	row := tx.QueryRowContext(ctx, pullRequestSelect+` WHERE repository_id=? AND number=?`, repositoryID, number)
	record, err := scanPullRequest(row)
	if err != nil {
		return PullRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return PullRequest{}, err
	}
	return record, nil
}

func (s *Store) DeletePullRequestCreation(ctx context.Context, repositoryID string, number int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM pull_requests WHERE repository_id=? AND number=? AND status=?`, repositoryID, number, PullRequestCreating)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return err
		}
		return errors.New("provisional pull request was not found")
	}
	return nil
}

func (s *Store) ProvisionalPullRequests(ctx context.Context, repositoryID string) ([]PullRequest, error) {
	rows, err := s.db.QueryContext(ctx, pullRequestSelect+` WHERE repository_id=? AND status=? ORDER BY number`, repositoryID, PullRequestCreating)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []PullRequest
	for rows.Next() {
		record, err := scanPullRequest(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) PullRequest(ctx context.Context, repositoryID string, number int64) (PullRequest, bool, error) {
	row := s.db.QueryRowContext(ctx, pullRequestSelect+` WHERE repository_id=? AND number=? AND status!='creating'`, repositoryID, number)
	record, err := scanPullRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PullRequest{}, false, nil
	}
	return record, err == nil, err
}

// OpenPullRequests returns a bounded newest-first set for automatic revision
// observation after Git writes.
func (s *Store) OpenPullRequests(ctx context.Context, repositoryID string, limit int) ([]PullRequest, bool, error) {
	if limit < 1 || limit > 1000 {
		return nil, false, errors.New("invalid open pull request limit")
	}
	rows, err := s.db.QueryContext(ctx, pullRequestSelect+` WHERE repository_id=? AND status=? ORDER BY number DESC LIMIT ?`, repositoryID, PullRequestOpen, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var records []PullRequest
	for rows.Next() {
		record, err := scanPullRequest(rows)
		if err != nil {
			return nil, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(records) > limit
	if more {
		records = records[:limit]
	}
	return records, more, nil
}

// OpenPullRequestsAfter returns one bounded circular page in ascending number
// order. Advancing the cursor to the last returned number gives every open
// request fair progress without deleting revision history.
func (s *Store) OpenPullRequestsAfter(ctx context.Context, repositoryID string, after int64, limit int) ([]PullRequest, bool, error) {
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, false, errors.New("invalid open pull request page")
	}
	rows, err := s.db.QueryContext(ctx, pullRequestSelect+` WHERE repository_id=? AND status=?
		ORDER BY CASE WHEN number>? THEN 0 ELSE 1 END,number ASC LIMIT ?`, repositoryID, PullRequestOpen, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var records []PullRequest
	for rows.Next() {
		record, err := scanPullRequest(rows)
		if err != nil {
			return nil, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(records) > limit
	if more {
		records = records[:limit]
	}
	return records, more, nil
}

func (s *Store) PullRequests(ctx context.Context, repositoryID string) ([]PullRequest, error) {
	rows, err := s.db.QueryContext(ctx, pullRequestSelect+` WHERE repository_id=? AND status!='creating' ORDER BY number`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []PullRequest
	for rows.Next() {
		record, err := scanPullRequest(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

const pullRequestSelect = `SELECT repository_id,number,title,source_branch,target_branch,status,created_at,updated_at,
	merge_source_oid,merge_target_oid,merge_oid,merge_receipt_ref,merged_at FROM pull_requests`

type rowScanner interface {
	Scan(...any) error
}

func scanPullRequest(scanner rowScanner) (PullRequest, error) {
	var record PullRequest
	var createdAt, updatedAt int64
	var mergedAt sql.NullInt64
	if err := scanner.Scan(
		&record.RepositoryID, &record.Number, &record.Title, &record.SourceBranch, &record.TargetBranch, &record.Status,
		&createdAt, &updatedAt, &record.MergeSourceOID, &record.MergeTargetOID, &record.MergeOID, &record.MergeReceipt, &mergedAt,
	); err != nil {
		return PullRequest{}, err
	}
	record.CreatedAt = unixTime(createdAt)
	record.UpdatedAt = unixTime(updatedAt)
	if mergedAt.Valid {
		value := unixTime(mergedAt.Int64)
		record.MergedAt = &value
	}
	return record, nil
}

func (s *Store) HasPullRequestRevision(ctx context.Context, repositoryID string, number int64, sourceOID, targetOID string) (bool, error) {
	var present int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM pull_request_revisions WHERE repository_id=? AND pull_request_number=? AND source_oid=? AND target_oid=?`,
		repositoryID, number, sourceOID, targetOID).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) RecordPullRequestRevision(ctx context.Context, revision PullRequestRevision) error {
	if !validObjectID(revision.SourceOID) || !validObjectID(revision.TargetOID) || revision.RecordedAt.IsZero() {
		return errors.New("invalid pull request revision")
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO pull_request_revisions(
		repository_id,pull_request_number,source_oid,target_oid,recorded_at
	) VALUES(?,?,?,?,?)`, revision.RepositoryID, revision.PullRequestNumber, revision.SourceOID, revision.TargetOID, revision.RecordedAt.Unix())
	return err
}

// LatestPullRequestRevisions returns a bounded newest-first set for automatic
// event reconciliation. The full recovery reader remains separate.
func (s *Store) LatestPullRequestRevisions(ctx context.Context, repositoryID string, limit int) ([]PullRequestRevision, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("invalid pull request revision limit")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT revisions.repository_id,revisions.pull_request_number,revisions.source_oid,revisions.target_oid,revisions.recorded_at
		FROM pull_request_revisions revisions
		JOIN pull_requests requests ON requests.repository_id=revisions.repository_id AND requests.number=revisions.pull_request_number
		WHERE revisions.repository_id=? AND requests.status!='creating'
		ORDER BY revisions.recorded_at DESC,revisions.pull_request_number DESC,revisions.source_oid DESC,revisions.target_oid DESC LIMIT ?`, repositoryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revisions []PullRequestRevision
	for rows.Next() {
		var revision PullRequestRevision
		var recordedAt int64
		if err := rows.Scan(&revision.RepositoryID, &revision.PullRequestNumber, &revision.SourceOID, &revision.TargetOID, &recordedAt); err != nil {
			return nil, err
		}
		revision.RecordedAt = unixTime(recordedAt)
		revisions = append(revisions, revision)
	}
	return revisions, rows.Err()
}

func (s *Store) PullRequestRevisions(ctx context.Context, repositoryID string) ([]PullRequestRevision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT revisions.repository_id,revisions.pull_request_number,revisions.source_oid,revisions.target_oid,revisions.recorded_at
		FROM pull_request_revisions revisions
		JOIN pull_requests requests ON requests.repository_id=revisions.repository_id AND requests.number=revisions.pull_request_number
		WHERE revisions.repository_id=? AND requests.status!='creating'
		ORDER BY revisions.pull_request_number,revisions.recorded_at,revisions.source_oid,revisions.target_oid`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revisions []PullRequestRevision
	for rows.Next() {
		var revision PullRequestRevision
		var recordedAt int64
		if err := rows.Scan(&revision.RepositoryID, &revision.PullRequestNumber, &revision.SourceOID, &revision.TargetOID, &recordedAt); err != nil {
			return nil, err
		}
		revision.RecordedAt = unixTime(recordedAt)
		revisions = append(revisions, revision)
	}
	return revisions, rows.Err()
}

func (s *Store) PullRequestRevisionsFor(ctx context.Context, repositoryID string, number int64) ([]PullRequestRevision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repository_id,pull_request_number,source_oid,target_oid,recorded_at
		FROM pull_request_revisions WHERE repository_id=? AND pull_request_number=? ORDER BY recorded_at,source_oid,target_oid`, repositoryID, number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revisions []PullRequestRevision
	for rows.Next() {
		var revision PullRequestRevision
		var recordedAt int64
		if err := rows.Scan(&revision.RepositoryID, &revision.PullRequestNumber, &revision.SourceOID, &revision.TargetOID, &recordedAt); err != nil {
			return nil, err
		}
		revision.RecordedAt = unixTime(recordedAt)
		revisions = append(revisions, revision)
	}
	return revisions, rows.Err()
}

func (s *Store) AppendPullRequestReview(ctx context.Context, review PullRequestReview) (PullRequestReview, error) {
	if err := validateReviewRecord(review, false); err != nil {
		return PullRequestReview{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PullRequestReview{}, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM pull_requests WHERE repository_id=? AND number=?`, review.RepositoryID, review.PullRequestNumber).Scan(&status); err != nil {
		return PullRequestReview{}, err
	}
	if status != PullRequestOpen {
		return PullRequestReview{}, errors.New("pull request is not open")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM pull_request_reviews WHERE repository_id=? AND pull_request_number=?`, review.RepositoryID, review.PullRequestNumber).Scan(&review.Sequence); err != nil {
		return PullRequestReview{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pull_request_reviews(
		repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,review_event_id,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?)`, review.RepositoryID, review.PullRequestNumber, review.Sequence, review.SourceOID, review.TargetOID, review.Status, review.ReviewerLabel, review.Provenance, review.ReviewEventID, review.CreatedAt.Unix()); err != nil {
		return PullRequestReview{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pull_requests SET updated_at=? WHERE repository_id=? AND number=?`, review.CreatedAt.Unix(), review.RepositoryID, review.PullRequestNumber); err != nil {
		return PullRequestReview{}, err
	}
	if err := tx.Commit(); err != nil {
		return PullRequestReview{}, err
	}
	return review, nil
}

func (s *Store) PullRequestReviewForRevision(ctx context.Context, repositoryID string, number int64, sourceOID, targetOID string) (PullRequestReview, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,review_event_id,created_at
		FROM pull_request_reviews WHERE repository_id=? AND pull_request_number=? AND source_oid=? AND target_oid=? ORDER BY sequence DESC LIMIT 1`, repositoryID, number, sourceOID, targetOID)
	review, err := scanPullRequestReview(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PullRequestReview{}, false, nil
	}
	return review, err == nil, err
}

func scanPullRequestReview(scanner rowScanner) (PullRequestReview, error) {
	var review PullRequestReview
	var createdAt int64
	if err := scanner.Scan(&review.RepositoryID, &review.PullRequestNumber, &review.Sequence, &review.SourceOID, &review.TargetOID, &review.Status, &review.ReviewerLabel, &review.Provenance, &review.ReviewEventID, &createdAt); err != nil {
		return PullRequestReview{}, err
	}
	review.CreatedAt = unixTime(createdAt)
	return review, nil
}

func (s *Store) BeginPullRequestMerge(ctx context.Context, intent PullRequestMergeIntent) (PullRequestMergeIntent, error) {
	if !validObjectID(intent.SourceOID) || !validObjectID(intent.TargetOID) || intent.ReceiptRef == "" || intent.CreatedAt.IsZero() {
		return PullRequestMergeIntent{}, errors.New("invalid merge intent")
	}
	intent.Mode = ""
	intent.TreeOID = ""
	intent.ResultOID = ""
	intent.Status = MergeIntentPreparing
	intent.UpdatedAt = intent.CreatedAt
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO pull_request_merge_intents(
		repository_id,pull_request_number,source_oid,target_oid,mode,tree_oid,result_oid,receipt_ref,status,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, intent.RepositoryID, intent.PullRequestNumber, intent.SourceOID, intent.TargetOID, intent.Mode, intent.TreeOID, intent.ResultOID, intent.ReceiptRef, intent.Status, intent.CreatedAt.Unix(), intent.UpdatedAt.Unix())
	if err != nil {
		return PullRequestMergeIntent{}, err
	}
	stored, ok, err := s.PullRequestMergeIntent(ctx, intent.RepositoryID, intent.PullRequestNumber, intent.SourceOID, intent.TargetOID)
	if err != nil {
		return PullRequestMergeIntent{}, err
	}
	if !ok || stored.ReceiptRef != intent.ReceiptRef {
		return PullRequestMergeIntent{}, errors.New("merge intent collision")
	}
	return stored, nil
}

func (s *Store) UpdatePullRequestMergeIntent(ctx context.Context, intent PullRequestMergeIntent) (PullRequestMergeIntent, error) {
	if err := validateMergeIntentRecord(intent, false); err != nil {
		return PullRequestMergeIntent{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE pull_request_merge_intents SET mode=?,tree_oid=?,result_oid=?,status=?,updated_at=?
		WHERE repository_id=? AND pull_request_number=? AND source_oid=? AND target_oid=? AND receipt_ref=?`,
		intent.Mode, intent.TreeOID, intent.ResultOID, intent.Status, intent.UpdatedAt.Unix(), intent.RepositoryID, intent.PullRequestNumber, intent.SourceOID, intent.TargetOID, intent.ReceiptRef)
	if err != nil {
		return PullRequestMergeIntent{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return PullRequestMergeIntent{}, err
		}
		return PullRequestMergeIntent{}, errors.New("merge intent was not found")
	}
	stored, _, err := s.PullRequestMergeIntent(ctx, intent.RepositoryID, intent.PullRequestNumber, intent.SourceOID, intent.TargetOID)
	return stored, err
}

func (s *Store) PullRequestMergeIntent(ctx context.Context, repositoryID string, number int64, sourceOID, targetOID string) (PullRequestMergeIntent, bool, error) {
	row := s.db.QueryRowContext(ctx, mergeIntentSelect+` WHERE repository_id=? AND pull_request_number=? AND source_oid=? AND target_oid=?`, repositoryID, number, sourceOID, targetOID)
	intent, err := scanMergeIntent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PullRequestMergeIntent{}, false, nil
	}
	return intent, err == nil, err
}

func (s *Store) PullRequestMergeIntents(ctx context.Context, incompleteOnly bool) ([]PullRequestMergeIntent, error) {
	query := mergeIntentSelect
	if incompleteOnly {
		query += ` WHERE status!='complete'`
	}
	query += ` ORDER BY repository_id,pull_request_number,created_at`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var intents []PullRequestMergeIntent
	for rows.Next() {
		intent, err := scanMergeIntent(rows)
		if err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	return intents, rows.Err()
}

const mergeIntentSelect = `SELECT repository_id,pull_request_number,source_oid,target_oid,mode,tree_oid,result_oid,receipt_ref,status,created_at,updated_at FROM pull_request_merge_intents`

func scanMergeIntent(scanner rowScanner) (PullRequestMergeIntent, error) {
	var intent PullRequestMergeIntent
	var createdAt, updatedAt int64
	if err := scanner.Scan(&intent.RepositoryID, &intent.PullRequestNumber, &intent.SourceOID, &intent.TargetOID, &intent.Mode, &intent.TreeOID, &intent.ResultOID, &intent.ReceiptRef, &intent.Status, &createdAt, &updatedAt); err != nil {
		return PullRequestMergeIntent{}, err
	}
	intent.CreatedAt = unixTime(createdAt)
	intent.UpdatedAt = unixTime(updatedAt)
	return intent, nil
}

func (s *Store) CompletePullRequestMerge(ctx context.Context, intent PullRequestMergeIntent, completedAt time.Time) error {
	if intent.Status != MergeIntentReady || intent.ResultOID == "" || completedAt.IsZero() {
		return errors.New("merge intent is not ready")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var storedStatus, resultOID, receiptRef string
	if err := tx.QueryRowContext(ctx, `SELECT status,result_oid,receipt_ref FROM pull_request_merge_intents
		WHERE repository_id=? AND pull_request_number=? AND source_oid=? AND target_oid=?`, intent.RepositoryID, intent.PullRequestNumber, intent.SourceOID, intent.TargetOID).Scan(&storedStatus, &resultOID, &receiptRef); err != nil {
		return err
	}
	if resultOID != intent.ResultOID || receiptRef != intent.ReceiptRef || (storedStatus != MergeIntentReady && storedStatus != MergeIntentComplete) {
		return errors.New("stored merge intent does not match the published result")
	}
	var requestStatus, mergedOID string
	if err := tx.QueryRowContext(ctx, `SELECT status,merge_oid FROM pull_requests WHERE repository_id=? AND number=?`, intent.RepositoryID, intent.PullRequestNumber).Scan(&requestStatus, &mergedOID); err != nil {
		return err
	}
	if requestStatus == PullRequestMerged {
		if mergedOID != intent.ResultOID {
			return errors.New("pull request is already merged with another result")
		}
	} else if requestStatus == PullRequestOpen {
		if _, err := tx.ExecContext(ctx, `UPDATE pull_requests SET status='merged',updated_at=?,merge_source_oid=?,merge_target_oid=?,merge_oid=?,merge_receipt_ref=?,merged_at=?
			WHERE repository_id=? AND number=? AND status='open'`, completedAt.Unix(), intent.SourceOID, intent.TargetOID, intent.ResultOID, intent.ReceiptRef, completedAt.Unix(), intent.RepositoryID, intent.PullRequestNumber); err != nil {
			return err
		}
	} else {
		return errors.New("pull request has an invalid state")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pull_request_merge_intents SET status='complete',updated_at=?
		WHERE repository_id=? AND pull_request_number=? AND source_oid=? AND target_oid=?`, completedAt.Unix(), intent.RepositoryID, intent.PullRequestNumber, intent.SourceOID, intent.TargetOID); err != nil {
		return err
	}
	return tx.Commit()
}

func readPullRequestRecovery(ctx context.Context, tx *sql.Tx, snapshot *RecoveryState) error {
	rows, err := tx.QueryContext(ctx, pullRequestSelect+` ORDER BY repository_id,number`)
	if err != nil {
		return err
	}
	for rows.Next() {
		record, err := scanPullRequest(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if record.Status == PullRequestCreating {
			rows.Close()
			return errors.New("provisional pull request creation must be reconciled before backup")
		}
		snapshot.PullRequests = append(snapshot.PullRequests, record)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, `SELECT repository_id,pull_request_number,source_oid,target_oid,recorded_at FROM pull_request_revisions ORDER BY repository_id,pull_request_number,recorded_at,source_oid,target_oid`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var revision PullRequestRevision
		var recordedAt int64
		if err := rows.Scan(&revision.RepositoryID, &revision.PullRequestNumber, &revision.SourceOID, &revision.TargetOID, &recordedAt); err != nil {
			rows.Close()
			return err
		}
		revision.RecordedAt = unixTime(recordedAt)
		snapshot.PullRequestRevisions = append(snapshot.PullRequestRevisions, revision)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, `SELECT repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,review_event_id,created_at FROM pull_request_reviews ORDER BY repository_id,pull_request_number,sequence`)
	if err != nil {
		return err
	}
	for rows.Next() {
		review, err := scanPullRequestReview(rows)
		if err != nil {
			rows.Close()
			return err
		}
		snapshot.PullRequestReviews = append(snapshot.PullRequestReviews, review)
	}
	if err := closeRows(rows); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, mergeIntentSelect+` ORDER BY repository_id,pull_request_number,created_at,source_oid,target_oid`)
	if err != nil {
		return err
	}
	for rows.Next() {
		intent, err := scanMergeIntent(rows)
		if err != nil {
			rows.Close()
			return err
		}
		snapshot.PullRequestMergeIntents = append(snapshot.PullRequestMergeIntents, intent)
	}
	return closeRows(rows)
}

func restorePullRequestRecovery(ctx context.Context, tx *sql.Tx, snapshot RecoveryState) error {
	for _, record := range snapshot.PullRequests {
		var mergedAt any
		if record.MergedAt != nil {
			mergedAt = record.MergedAt.Unix()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO pull_requests(
			repository_id,number,title,source_branch,target_branch,status,created_at,updated_at,merge_source_oid,merge_target_oid,merge_oid,merge_receipt_ref,merged_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, record.RepositoryID, record.Number, record.Title, record.SourceBranch, record.TargetBranch, record.Status, record.CreatedAt.Unix(), record.UpdatedAt.Unix(), record.MergeSourceOID, record.MergeTargetOID, record.MergeOID, record.MergeReceipt, mergedAt); err != nil {
			return fmt.Errorf("restore pull request %s#%d: %w", record.RepositoryID, record.Number, err)
		}
	}
	for _, revision := range snapshot.PullRequestRevisions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pull_request_revisions(repository_id,pull_request_number,source_oid,target_oid,recorded_at) VALUES(?,?,?,?,?)`, revision.RepositoryID, revision.PullRequestNumber, revision.SourceOID, revision.TargetOID, revision.RecordedAt.Unix()); err != nil {
			return fmt.Errorf("restore pull request revision: %w", err)
		}
	}
	for _, review := range snapshot.PullRequestReviews {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pull_request_reviews(repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,review_event_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, review.RepositoryID, review.PullRequestNumber, review.Sequence, review.SourceOID, review.TargetOID, review.Status, review.ReviewerLabel, review.Provenance, review.ReviewEventID, review.CreatedAt.Unix()); err != nil {
			return fmt.Errorf("restore pull request review: %w", err)
		}
	}
	for _, intent := range snapshot.PullRequestMergeIntents {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pull_request_merge_intents(repository_id,pull_request_number,source_oid,target_oid,mode,tree_oid,result_oid,receipt_ref,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, intent.RepositoryID, intent.PullRequestNumber, intent.SourceOID, intent.TargetOID, intent.Mode, intent.TreeOID, intent.ResultOID, intent.ReceiptRef, intent.Status, intent.CreatedAt.Unix(), intent.UpdatedAt.Unix()); err != nil {
			return fmt.Errorf("restore pull request merge intent: %w", err)
		}
	}
	return nil
}

func ValidatePullRequestRecovery(snapshot RecoveryState) error {
	repositories := make(map[string]bool, len(snapshot.Repositories))
	for _, repository := range snapshot.Repositories {
		repositories[repository.ID] = true
	}
	requests := make(map[string]PullRequest, len(snapshot.PullRequests))
	for _, record := range snapshot.PullRequests {
		if !repositories[record.RepositoryID] || record.Number <= 0 {
			return errors.New("pull request refers to an unknown repository")
		}
		if err := validatePullRequestRecord(record); err != nil {
			return err
		}
		if record.Status == PullRequestCreating {
			return errors.New("backup contains a provisional pull request creation")
		}
		key := pullRequestKey(record.RepositoryID, record.Number)
		if _, exists := requests[key]; exists {
			return errors.New("duplicate pull request record")
		}
		requests[key] = record
	}
	revisions := make(map[string]bool, len(snapshot.PullRequestRevisions))
	requestRevisionCounts := make(map[string]int)
	for _, revision := range snapshot.PullRequestRevisions {
		requestKey := pullRequestKey(revision.RepositoryID, revision.PullRequestNumber)
		if _, exists := requests[requestKey]; !exists || !validObjectID(revision.SourceOID) || !validObjectID(revision.TargetOID) || revision.RecordedAt.IsZero() {
			return errors.New("invalid pull request revision record")
		}
		key := revisionKey(revision.RepositoryID, revision.PullRequestNumber, revision.SourceOID, revision.TargetOID)
		if revisions[key] {
			return errors.New("duplicate pull request revision record")
		}
		revisions[key] = true
		requestRevisionCounts[requestKey]++
	}
	for key := range requests {
		if requestRevisionCounts[key] == 0 {
			return errors.New("pull request has no retained revision binding")
		}
	}
	reviews := make(map[string]bool, len(snapshot.PullRequestReviews))
	reviewEvents := make(map[string]bool)
	reviewCounts := make(map[string]int)
	for _, review := range snapshot.PullRequestReviews {
		if _, exists := requests[pullRequestKey(review.RepositoryID, review.PullRequestNumber)]; !exists || !revisions[revisionKey(review.RepositoryID, review.PullRequestNumber, review.SourceOID, review.TargetOID)] {
			return errors.New("pull request review refers to an unknown revision")
		}
		if err := validateReviewRecord(review, true); err != nil {
			return err
		}
		key := pullRequestKey(review.RepositoryID, review.PullRequestNumber) + "/" + strconv.FormatInt(review.Sequence, 10)
		if reviews[key] {
			return errors.New("duplicate pull request review sequence")
		}
		reviews[key] = true
		if review.ReviewEventID != "" {
			eventKey := review.RepositoryID + "/" + review.ReviewEventID
			if reviewEvents[eventKey] {
				return errors.New("duplicate pull request review event identity")
			}
			reviewEvents[eventKey] = true
		}
		reviewCounts[pullRequestKey(review.RepositoryID, review.PullRequestNumber)]++
	}
	for key := range requests {
		if reviewCounts[key] == 0 {
			return errors.New("pull request has no explicit review choice")
		}
	}
	completed := make(map[string]PullRequestMergeIntent)
	intents := make(map[string]bool, len(snapshot.PullRequestMergeIntents))
	for _, intent := range snapshot.PullRequestMergeIntents {
		requestKey := pullRequestKey(intent.RepositoryID, intent.PullRequestNumber)
		if _, exists := requests[requestKey]; !exists || !revisions[revisionKey(intent.RepositoryID, intent.PullRequestNumber, intent.SourceOID, intent.TargetOID)] {
			return errors.New("merge intent refers to an unknown pull request revision")
		}
		if err := validateMergeIntentRecord(intent, true); err != nil {
			return err
		}
		key := revisionKey(intent.RepositoryID, intent.PullRequestNumber, intent.SourceOID, intent.TargetOID)
		if intents[key] {
			return errors.New("duplicate pull request merge intent")
		}
		intents[key] = true
		if intent.Status == MergeIntentComplete {
			if _, exists := completed[requestKey]; exists {
				return errors.New("pull request has multiple completed merge intents")
			}
			completed[requestKey] = intent
		}
	}
	for key, record := range requests {
		if record.Status != PullRequestMerged {
			continue
		}
		intent, ok := completed[key]
		if !ok || intent.SourceOID != record.MergeSourceOID || intent.TargetOID != record.MergeTargetOID || intent.ResultOID != record.MergeOID || intent.ReceiptRef != record.MergeReceipt {
			return errors.New("merged pull request does not match a completed merge intent")
		}
	}
	return nil
}

func validatePullRequestRecord(record PullRequest) error {
	if record.RepositoryID == "" || record.Number < 0 || !validText(record.Title, 500) || !validBranchText(record.SourceBranch) || !validBranchText(record.TargetBranch) || record.SourceBranch == record.TargetBranch || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return errors.New("invalid pull request metadata")
	}
	switch record.Status {
	case PullRequestCreating, PullRequestOpen:
		if record.MergeSourceOID != "" || record.MergeTargetOID != "" || record.MergeOID != "" || record.MergeReceipt != "" || record.MergedAt != nil {
			return errors.New("unmerged pull request contains merge metadata")
		}
	case PullRequestMerged:
		if !validObjectID(record.MergeSourceOID) || !validObjectID(record.MergeTargetOID) || !validObjectID(record.MergeOID) || record.MergeReceipt == "" || record.MergedAt == nil || record.MergedAt.IsZero() {
			return errors.New("merged pull request metadata is incomplete")
		}
	default:
		return errors.New("invalid pull request status")
	}
	return nil
}

// validReviewEventID accepts the 32 lowercase hexadecimal characters an event
// identity always had.
func validReviewEventID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func validateReviewRecord(review PullRequestReview, requireSequence bool) error {
	if review.RepositoryID == "" || review.PullRequestNumber <= 0 || (requireSequence && review.Sequence <= 0) || !validObjectID(review.SourceOID) || !validObjectID(review.TargetOID) || review.CreatedAt.IsZero() {
		return errors.New("invalid pull request review metadata")
	}
	if review.ReviewEventID != "" && !validReviewEventID(review.ReviewEventID) {
		return errors.New("invalid pull request review event identity")
	}
	switch review.Status {
	case ReviewPending:
		if review.ReviewerLabel != "" || review.Provenance != ReviewProvenanceRequest {
			return errors.New("invalid pending review provenance")
		}
	case ReviewSkipped:
		if review.ReviewEventID != "" {
			return errors.New("only pending review requests can carry an event identity")
		}
		if review.ReviewerLabel != "" || review.Provenance != ReviewProvenanceSkip {
			return errors.New("invalid skipped review provenance")
		}
	case ReviewNotRequested:
		if review.ReviewEventID != "" {
			return errors.New("only pending review requests can carry an event identity")
		}
		if review.ReviewerLabel != "" || review.Provenance != ReviewProvenanceDefault {
			return errors.New("invalid default review provenance")
		}
	case ReviewApproved, ReviewChangesRequested:
		if review.ReviewEventID != "" {
			return errors.New("only pending review requests can carry an event identity")
		}
		if !validText(review.ReviewerLabel, 200) || review.Provenance != ReviewProvenanceExternalTool {
			return errors.New("invalid submitted review provenance")
		}
	default:
		return errors.New("invalid pull request review status")
	}
	return nil
}

func validateMergeIntentRecord(intent PullRequestMergeIntent, requireTimes bool) error {
	if intent.RepositoryID == "" || intent.PullRequestNumber <= 0 || !validObjectID(intent.SourceOID) || !validObjectID(intent.TargetOID) || intent.ReceiptRef == "" {
		return errors.New("invalid pull request merge intent")
	}
	if requireTimes && (intent.CreatedAt.IsZero() || intent.UpdatedAt.IsZero() || intent.UpdatedAt.Before(intent.CreatedAt)) {
		return errors.New("invalid pull request merge intent time")
	}
	expectedReceipt := "refs/owngit/pull-requests/" + strconv.FormatInt(intent.PullRequestNumber, 10) + "/merge-receipt"
	if intent.ReceiptRef != expectedReceipt {
		return errors.New("invalid pull request merge receipt ref")
	}
	switch intent.Status {
	case MergeIntentPreparing:
		if intent.Mode != "" || intent.TreeOID != "" || intent.ResultOID != "" {
			return errors.New("preparing merge intent contains a result")
		}
	case MergeIntentPlanned:
		switch intent.Mode {
		case "fast_forward":
			if intent.ResultOID != intent.SourceOID || intent.TreeOID != "" {
				return errors.New("invalid planned fast-forward merge intent")
			}
		case "merge_commit":
			if !validObjectID(intent.TreeOID) || intent.ResultOID != "" {
				return errors.New("invalid planned merge-commit intent")
			}
		default:
			return errors.New("invalid planned merge intent mode")
		}
	case MergeIntentReady, MergeIntentComplete:
		switch intent.Mode {
		case "fast_forward":
			if intent.ResultOID != intent.SourceOID || intent.TreeOID != "" {
				return errors.New("invalid fast-forward merge intent")
			}
		case "merge_commit":
			if !validObjectID(intent.TreeOID) || !validObjectID(intent.ResultOID) {
				return errors.New("invalid merge-commit intent")
			}
		default:
			return errors.New("invalid merge intent mode")
		}
	default:
		return errors.New("invalid merge intent status")
	}
	return nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func validText(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validBranchText(value string) bool {
	if value == "" || len(value) > 255 || value != strings.TrimSpace(value) || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, "\\ ~^:?*[") {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func pullRequestKey(repositoryID string, number int64) string {
	return repositoryID + "/" + strconv.FormatInt(number, 10)
}

func revisionKey(repositoryID string, number int64, sourceOID, targetOID string) string {
	return pullRequestKey(repositoryID, number) + "/" + sourceOID + "/" + targetOID
}
