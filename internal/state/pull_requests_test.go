package state

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenUpgradesAcceptedPrePullRequestStateWithoutLosingData(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "legacy-state")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", sqliteFileURI(filepath.Join(directory, databaseName)))
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE passwords (kind TEXT PRIMARY KEY CHECK (kind IN ('access','admin')), encoded TEXT NOT NULL)`,
		`CREATE TABLE sessions (token_hash BLOB PRIMARY KEY, kind TEXT NOT NULL CHECK (kind IN ('setup','general','admin')), csrf TEXT NOT NULL, version INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
		`CREATE TABLE bootstrap (singleton INTEGER PRIMARY KEY CHECK (singleton = 1), token_hash BLOB NOT NULL, expires_at INTEGER NOT NULL)`,
		`CREATE TABLE login_attempts (kind TEXT NOT NULL, address TEXT NOT NULL, window_started_at INTEGER NOT NULL, attempts INTEGER NOT NULL, blocked_until INTEGER NOT NULL, PRIMARY KEY (kind,address))`,
		`CREATE TABLE repositories (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE trusted_hosts (host TEXT PRIMARY KEY, created_at INTEGER NOT NULL)`,
		`INSERT INTO metadata(key,value) VALUES ('initialized','true'),('repository_root','/accepted/repositories'),('access_mode','open'),('access_session_version','4'),('admin_session_version','7'),('insecure_http_accepted','false')`,
		`INSERT INTO passwords(kind,encoded) VALUES ('admin','accepted-admin-hash')`,
		`INSERT INTO repositories(id,name,description,created_at) VALUES ('legacy','Legacy repository','preserve me',1800000000)`,
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	settings, err := store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Initialized || settings.RepositoryRoot != "/accepted/repositories" || settings.AccessSessionVersion != 4 || settings.AdminSessionVersion != 7 {
		t.Fatalf("upgraded settings=%+v", settings)
	}
	repositories, err := store.Repositories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0].ID != "legacy" || repositories[0].Description != "preserve me" {
		t.Fatalf("upgraded repositories=%+v", repositories)
	}
	now := time.Unix(1_900_000_000, 0).UTC()
	provisional, err := store.BeginPullRequestCreation(ctx, "legacy", "First pull request", "feature", "main", strings.Repeat("a", 40), strings.Repeat("b", 40), ReviewSkipped, now)
	if err != nil {
		t.Fatalf("create provisional pull request after upgrade: %v", err)
	}
	activated, err := store.ActivatePullRequestCreation(ctx, "legacy", provisional.Number, now)
	if err != nil {
		t.Fatalf("activate pull request after upgrade: %v", err)
	}
	if activated.Number != 1 || activated.Status != PullRequestOpen {
		t.Fatalf("pull request after upgrade=%+v", activated)
	}
}

func TestPullRequestNumbersAreDurableAndPerRepository(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	for _, repository := range []Repository{
		{ID: "alpha", Name: "Alpha", CreatedAt: now},
		{ID: "beta", Name: "Beta", CreatedAt: now},
	} {
		if err := store.AddRepository(ctx, repository); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	sourceOID := strings.Repeat("a", 40)
	targetOID := strings.Repeat("b", 40)
	first, err := store.CreatePullRequest(ctx, "alpha", "First", "feature-one", "main", sourceOID, targetOID, ReviewPending, now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	second, err := store.CreatePullRequest(ctx, "alpha", "Second", "feature-two", "main", sourceOID, targetOID, ReviewSkipped, now.Add(time.Second))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	other, err := store.CreatePullRequest(ctx, "beta", "Other", "feature", "main", sourceOID, targetOID, ReviewPending, now)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if first.Number != 1 || second.Number != 2 || other.Number != 1 {
		store.Close()
		t.Fatalf("pull request numbers: alpha=%d,%d beta=%d", first.Number, second.Number, other.Number)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, exists, err := reopened.PullRequest(ctx, "alpha", second.Number)
	if err != nil || !exists || stored.Title != second.Title || stored.SourceBranch != second.SourceBranch {
		t.Fatalf("durable pull request=%+v exists=%v err=%v", stored, exists, err)
	}
	review, exists, err := reopened.PullRequestReviewForRevision(ctx, "alpha", second.Number, sourceOID, targetOID)
	if err != nil || !exists || review.Status != ReviewSkipped || review.Provenance != ReviewProvenanceSkip {
		t.Fatalf("durable review=%+v exists=%v err=%v", review, exists, err)
	}
}
