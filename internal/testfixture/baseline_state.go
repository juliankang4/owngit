// Package testfixture provides source-controlled compatibility fixtures.
package testfixture

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const (
	// CommittedBaselineStateCommit is the revision from which the DDL below was
	// copied. Keeping the DDL here makes compatibility tests independent of Git
	// history and shallow clones.
	CommittedBaselineStateCommit = "37b54e394a3292bc94383a0e7e1d19af85f9ab52"
	BaselineRepositoryID         = "project"
	BaselinePullRequestNumber    = int64(1)
	BaselineSessionToken         = "synthetic-baseline-session"
	BaselineSourceOID            = "1111111111111111111111111111111111111111"
	BaselineTargetOID            = "2222222222222222222222222222222222222222"
)

// BaselineStateOptions supplies data that must match a test repository while
// preserving the committed baseline schema.
type BaselineStateOptions struct {
	RepositoryRoot    string
	AdminPasswordHash string
	SourceOID         string
	TargetOID         string
}

// CreateCommittedBaselineState writes the exact unversioned table definitions
// emitted by OwnGit commit 37b54e394a3292bc94383a0e7e1d19af85f9ab52. The
// fixture includes settings, authority, a repository, and pull request history.
func CreateCommittedBaselineState(ctx context.Context, directory string, options BaselineStateOptions) error {
	if options.RepositoryRoot == "" || options.AdminPasswordHash == "" {
		return fmt.Errorf("baseline state options are incomplete")
	}
	if options.SourceOID == "" {
		options.SourceOID = BaselineSourceOID
	}
	if options.TargetOID == "" {
		options.TargetOID = BaselineTargetOID
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	path := filepath.Join(directory, "owngit.sqlite")
	db, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	closeDatabase := true
	defer func() {
		if closeDatabase {
			_ = db.Close()
		}
	}()
	for _, statement := range committedBaselineDDL {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create committed baseline schema: %w", err)
		}
	}

	const createdAt = int64(1_700_000_000)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	metadata := map[string]string{
		"initialized":            "true",
		"repository_root":        options.RepositoryRoot,
		"access_mode":            "open",
		"access_session_version": "3",
		"admin_session_version":  "4",
		"insecure_http_accepted": "true",
	}
	for key, value := range metadata {
		if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?)`, key, value); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO passwords(kind,encoded) VALUES('admin',?)`, options.AdminPasswordHash); err != nil {
		return err
	}
	sessionHash := sha256.Sum256([]byte(BaselineSessionToken))
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(token_hash,kind,csrf,version,expires_at) VALUES(?,'admin','baseline-csrf',4,2000000000)`, sessionHash[:]); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO login_attempts(kind,address,window_started_at,attempts,blocked_until) VALUES('admin','127.0.0.1',?,2,0)`, createdAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repositories(id,name,description,created_at) VALUES(?,?,?,?)`, BaselineRepositoryID, "Baseline project", "Committed baseline fixture", createdAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pull_requests(repository_id,number,title,source_branch,target_branch,status,created_at,updated_at) VALUES(?,?,'Baseline pull request','feature','main','open',?,?)`, BaselineRepositoryID, BaselinePullRequestNumber, createdAt, createdAt+10); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pull_request_revisions(repository_id,pull_request_number,source_oid,target_oid,recorded_at) VALUES(?,?,?,?,?)`, BaselineRepositoryID, BaselinePullRequestNumber, options.SourceOID, options.TargetOID, createdAt+5); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pull_request_reviews(repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,created_at) VALUES(?,?,?,?,?,'approved','baseline-reviewer','supplied_external_tool',?)`, BaselineRepositoryID, BaselinePullRequestNumber, 1, options.SourceOID, options.TargetOID, createdAt+6); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO trusted_hosts(host,created_at) VALUES('baseline.example.invalid',?)`, createdAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	closeDatabase = false
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func sqliteURI(path string) string {
	slashPath := filepath.ToSlash(path)
	if len(slashPath) >= 3 && slashPath[1] == ':' && slashPath[2] == '/' &&
		(('a' <= slashPath[0] && slashPath[0] <= 'z') || ('A' <= slashPath[0] && slashPath[0] <= 'Z')) {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

var committedBaselineDDL = []string{
	`PRAGMA journal_mode=WAL`,
	`PRAGMA foreign_keys=ON`,
	`PRAGMA busy_timeout=5000`,
	`CREATE TABLE IF NOT EXISTS metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS passwords (
		kind TEXT PRIMARY KEY CHECK (kind IN ('access','admin')),
		encoded TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		token_hash BLOB PRIMARY KEY,
		kind TEXT NOT NULL CHECK (kind IN ('setup','general','admin')),
		csrf TEXT NOT NULL,
		version INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS bootstrap (
		singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
		token_hash BLOB NOT NULL,
		expires_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS login_attempts (
		kind TEXT NOT NULL,
		address TEXT NOT NULL,
		window_started_at INTEGER NOT NULL,
		attempts INTEGER NOT NULL,
		blocked_until INTEGER NOT NULL,
		PRIMARY KEY (kind, address)
	)`,
	`CREATE TABLE IF NOT EXISTS repositories (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		description TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS pull_requests (
		repository_id TEXT NOT NULL,
		number INTEGER NOT NULL CHECK (number > 0),
		title TEXT NOT NULL,
		source_branch TEXT NOT NULL,
		target_branch TEXT NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('creating','open','merged')),
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		merge_source_oid TEXT NOT NULL DEFAULT '',
		merge_target_oid TEXT NOT NULL DEFAULT '',
		merge_oid TEXT NOT NULL DEFAULT '',
		merge_receipt_ref TEXT NOT NULL DEFAULT '',
		merged_at INTEGER,
		PRIMARY KEY (repository_id, number),
		FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS pull_request_revisions (
		repository_id TEXT NOT NULL,
		pull_request_number INTEGER NOT NULL,
		source_oid TEXT NOT NULL,
		target_oid TEXT NOT NULL,
		recorded_at INTEGER NOT NULL,
		PRIMARY KEY (repository_id, pull_request_number, source_oid, target_oid),
		FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS pull_request_reviews (
		repository_id TEXT NOT NULL,
		pull_request_number INTEGER NOT NULL,
		sequence INTEGER NOT NULL CHECK (sequence > 0),
		source_oid TEXT NOT NULL,
		target_oid TEXT NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('pending','approved','changes_requested','skipped')),
		reviewer_label TEXT NOT NULL,
		provenance TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (repository_id, pull_request_number, sequence),
		FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS pull_request_merge_intents (
		repository_id TEXT NOT NULL,
		pull_request_number INTEGER NOT NULL,
		source_oid TEXT NOT NULL,
		target_oid TEXT NOT NULL,
		mode TEXT NOT NULL,
		tree_oid TEXT NOT NULL,
		result_oid TEXT NOT NULL,
		receipt_ref TEXT NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('preparing','planned','ready','complete')),
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (repository_id, pull_request_number, source_oid, target_oid),
		FOREIGN KEY (repository_id, pull_request_number) REFERENCES pull_requests(repository_id, number) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS trusted_hosts (
		host TEXT PRIMARY KEY,
		created_at INTEGER NOT NULL
	)`,
}
