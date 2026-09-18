package state

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type RecoveryState struct {
	AccessMode              string
	AccessPasswordHash      string
	AdminPasswordHash       string
	Repositories            []Repository
	PullRequests            []PullRequest
	PullRequestRevisions    []PullRequestRevision
	PullRequestReviews      []PullRequestReview
	PullRequestMergeIntents []PullRequestMergeIntent
}

// RecoverySnapshot reads all portable state in one database transaction.
// Sessions, setup capabilities, login attempts, trusted hosts, repository-root
// paths, and insecure-transport consent are intentionally excluded.
func (s *Store) RecoverySnapshot(ctx context.Context) (RecoveryState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecoveryState{}, err
	}
	defer tx.Rollback()

	values := make(map[string]string)
	rows, err := tx.QueryContext(ctx, `SELECT key,value FROM metadata WHERE key IN ('initialized','access_mode')`)
	if err != nil {
		return RecoveryState{}, err
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return RecoveryState{}, err
		}
		values[key] = value
	}
	if err := rows.Close(); err != nil {
		return RecoveryState{}, err
	}
	if values["initialized"] != "true" {
		return RecoveryState{}, errors.New("setup is not complete")
	}

	snapshot := RecoveryState{AccessMode: values["access_mode"]}
	passwords, err := tx.QueryContext(ctx, `SELECT kind,encoded FROM passwords ORDER BY kind`)
	if err != nil {
		return RecoveryState{}, err
	}
	for passwords.Next() {
		var kind, encoded string
		if err := passwords.Scan(&kind, &encoded); err != nil {
			passwords.Close()
			return RecoveryState{}, err
		}
		switch kind {
		case "access":
			snapshot.AccessPasswordHash = encoded
		case "admin":
			snapshot.AdminPasswordHash = encoded
		}
	}
	if err := passwords.Close(); err != nil {
		return RecoveryState{}, err
	}

	repositories, err := tx.QueryContext(ctx, `SELECT id,name,description,created_at FROM repositories ORDER BY id`)
	if err != nil {
		return RecoveryState{}, err
	}
	for repositories.Next() {
		var repository Repository
		var createdAt int64
		if err := repositories.Scan(&repository.ID, &repository.Name, &repository.Description, &createdAt); err != nil {
			repositories.Close()
			return RecoveryState{}, err
		}
		repository.CreatedAt = unixTime(createdAt)
		snapshot.Repositories = append(snapshot.Repositories, repository)
	}
	if err := repositories.Close(); err != nil {
		return RecoveryState{}, err
	}
	if snapshot.AdminPasswordHash == "" || (snapshot.AccessMode == "password" && snapshot.AccessPasswordHash == "") {
		return RecoveryState{}, errors.New("portable password state is incomplete")
	}
	if snapshot.AccessMode != "open" && snapshot.AccessMode != "password" {
		return RecoveryState{}, errors.New("portable access mode is invalid")
	}
	if err := readPullRequestRecovery(ctx, tx, &snapshot); err != nil {
		return RecoveryState{}, err
	}
	if err := ValidatePullRequestRecovery(snapshot); err != nil {
		return RecoveryState{}, fmt.Errorf("portable pull request state is invalid: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RecoveryState{}, err
	}
	return snapshot, nil
}

// RestoreRecoveryState replaces a new store's portable state in one
// transaction. Every machine-bound or live credential table is cleared.
func (s *Store) RestoreRecoveryState(ctx context.Context, repositoryRoot string, snapshot RecoveryState) error {
	if repositoryRoot == "" {
		return errors.New("repository root is required")
	}
	if snapshot.AccessMode != "open" && snapshot.AccessMode != "password" {
		return errors.New("invalid recovery access mode")
	}
	if snapshot.AdminPasswordHash == "" || (snapshot.AccessMode == "password" && snapshot.AccessPasswordHash == "") {
		return errors.New("recovery password hashes are incomplete")
	}
	if err := ValidatePullRequestRecovery(snapshot); err != nil {
		return fmt.Errorf("invalid recovered pull request state: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var initialized string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key='initialized'`).Scan(&initialized); err != nil {
		return err
	}
	var repositoryCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories`).Scan(&repositoryCount); err != nil {
		return err
	}
	if initialized != "false" || repositoryCount != 0 {
		return errors.New("recovery state destination is not new")
	}

	metadata := map[string]string{
		"initialized":            "true",
		"repository_root":        repositoryRoot,
		"access_mode":            snapshot.AccessMode,
		"access_session_version": "1",
		"admin_session_version":  "1",
		"insecure_http_accepted": "false",
	}
	for key, value := range metadata {
		if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	for _, table := range []string{"pull_request_merge_intents", "pull_request_reviews", "pull_request_revisions", "pull_requests", "sessions", "bootstrap", "login_attempts", "trusted_hosts", "passwords", "repositories"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO passwords(kind,encoded) VALUES('admin',?)`, snapshot.AdminPasswordHash); err != nil {
		return err
	}
	if snapshot.AccessMode == "password" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO passwords(kind,encoded) VALUES('access',?)`, snapshot.AccessPasswordHash); err != nil {
			return err
		}
	}
	for _, repository := range snapshot.Repositories {
		if repository.ID == "" || repository.Name == "" {
			return errors.New("invalid recovered repository metadata")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO repositories(id,name,description,created_at) VALUES(?,?,?,?)`, repository.ID, repository.Name, repository.Description, repository.CreatedAt.Unix()); err != nil {
			return fmt.Errorf("restore repository %q metadata: %w", repository.ID, err)
		}
	}
	if err := restorePullRequestRecovery(ctx, tx, snapshot); err != nil {
		return err
	}
	return tx.Commit()
}

func unixTime(seconds int64) time.Time {
	return time.Unix(seconds, 0)
}
