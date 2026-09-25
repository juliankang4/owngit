package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Schema 14 is what the 1.0 releases wrote. It upgrades in place to the
// current schema, which admits closed pull requests, and the rebuilt pull
// request table keeps every row that refers to it.
func TestReleasedSchemaUpgradesAndKeepsPullRequestHistory(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	createMigratedSchemaDatabase(t, directory, releasedSchemaVersion)
	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	source, target := "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	for _, statement := range []string{
		`INSERT INTO repositories(id,name,description,created_at) VALUES('project','project','',1)`,
		`INSERT INTO pull_requests(repository_id,number,title,source_branch,target_branch,status,created_at,updated_at) VALUES('project',1,'Kept','feature','main','open',1,2)`,
		`INSERT INTO pull_request_revisions(repository_id,pull_request_number,source_oid,target_oid,recorded_at) VALUES('project',1,'` + source + `','` + target + `',1)`,
		`INSERT INTO pull_request_reviews(repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,created_at) VALUES('project',1,1,'` + source + `','` + target + `','not_requested','','default',1)`,
		`INSERT INTO pull_request_merge_intents(repository_id,pull_request_number,source_oid,target_oid,mode,tree_oid,result_oid,receipt_ref,status,created_at,updated_at) VALUES('project',1,'` + source + `','` + target + `','','','','refs/owngit/pull-requests/1/merge-receipt','preparing',1,1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	noErr(t, db.Close())

	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("open released schema: %v", err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("upgraded schema version=%d err=%v", version, err)
	}
	if fingerprint, objects, err := schemaFingerprint(ctx, store.db); err != nil || fingerprint != currentSchemaFingerprint || objects != currentSchemaObjects {
		t.Fatalf("upgraded schema fingerprint=%s objects=%d err=%v", fingerprint, objects, err)
	}
	var requests, revisions, reviews, intents int
	noErr(t, store.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM pull_requests),(SELECT COUNT(*) FROM pull_request_revisions),(SELECT COUNT(*) FROM pull_request_reviews),(SELECT COUNT(*) FROM pull_request_merge_intents)`).Scan(&requests, &revisions, &reviews, &intents))
	if requests != 1 || revisions != 1 || reviews != 1 || intents != 1 {
		t.Fatalf("upgrade lost pull request history: requests=%d revisions=%d reviews=%d intents=%d", requests, revisions, reviews, intents)
	}
	var enforced int
	noErr(t, store.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enforced))
	if enforced != 1 {
		t.Fatal("foreign key enforcement was not restored after the migration")
	}
	closed, err := store.SetPullRequestClosed(ctx, "project", 1, true, time.Unix(3, 0))
	if err != nil || closed.Status != PullRequestClosed {
		t.Fatalf("close after upgrade=%+v err=%v", closed, err)
	}
	// Deleting the repository still removes the pull request and its rows.
	noErr(t, store.Exec(ctx, `DELETE FROM repositories WHERE id='project'`))
	var remaining int
	noErr(t, store.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM pull_requests)+(SELECT COUNT(*) FROM pull_request_revisions)+(SELECT COUNT(*) FROM pull_request_reviews)+(SELECT COUNT(*) FROM pull_request_merge_intents)`).Scan(&remaining))
	if remaining != 0 {
		t.Fatalf("cascading delete left %d pull request rows", remaining)
	}
}

func TestPullRequestCloseAndReopenTransitions(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	defer store.Close()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "synthetic-admin-hash", true))
	noErr(t, store.Exec(ctx, `INSERT INTO repositories(id,name,description,created_at) VALUES('project','project','',1)`))
	record, err := store.CreatePullRequest(ctx, "project", "Close me", "feature", "main", "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222", ReviewNotRequested, time.Unix(10, 0))
	noErr(t, err)
	if _, err := store.SetPullRequestClosed(ctx, "project", record.Number, false, time.Unix(11, 0)); err != ErrPullRequestStateChanged {
		t.Fatalf("reopen of an open pull request err=%v", err)
	}
	closed, err := store.SetPullRequestClosed(ctx, "project", record.Number, true, time.Unix(12, 0))
	if err != nil || closed.Status != PullRequestClosed || closed.UpdatedAt.Unix() != 12 {
		t.Fatalf("close=%+v err=%v", closed, err)
	}
	if open, more, err := store.OpenPullRequests(ctx, "project", 10); err != nil || more || len(open) != 0 {
		t.Fatalf("a closed pull request is listed as open: %d err=%v", len(open), err)
	}
	if _, exists, err := store.OpenPullRequestForBranches(ctx, "project", "feature", "main"); err != nil || exists {
		t.Fatalf("a closed pull request still holds its branch pair: exists=%v err=%v", exists, err)
	}
	if _, err := store.AppendPullRequestReview(ctx, PullRequestReview{RepositoryID: "project", PullRequestNumber: record.Number,
		SourceOID: "1111111111111111111111111111111111111111", TargetOID: "2222222222222222222222222222222222222222",
		Status: ReviewSkipped, Provenance: ReviewProvenanceSkip, CreatedAt: time.Unix(13, 0)}); err == nil {
		t.Fatal("a review was recorded on a closed pull request")
	}
	snapshot, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	noErr(t, ValidatePullRequestRecovery(snapshot))
	reopened, err := store.SetPullRequestClosed(ctx, "project", record.Number, false, time.Unix(14, 0))
	if err != nil || reopened.Status != PullRequestOpen {
		t.Fatalf("reopen=%+v err=%v", reopened, err)
	}
}

// Open describes the upgrade it applied, once. A new database, a current one,
// and one that another opener already upgraded report nothing.
func TestSchemaUpgradeIsReportedOnce(t *testing.T) {
	ctx := context.Background()
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	if upgrade := fresh.SchemaUpgrade(); upgrade != "" {
		t.Fatalf("new database reported %q", upgrade)
	}
	noErr(t, fresh.Close())

	directory := filepath.Join(t.TempDir(), "state")
	createMigratedSchemaDatabase(t, directory, releasedSchemaVersion)
	store, err := Open(ctx, directory)
	noErr(t, err)
	if upgrade := store.SchemaUpgrade(); upgrade != "state database upgraded from schema 14 to 15" {
		t.Fatalf("released schema upgrade reported %q", upgrade)
	}
	noErr(t, store.Close())

	reopened, err := Open(ctx, directory)
	noErr(t, err)
	defer reopened.Close()
	if upgrade := reopened.SchemaUpgrade(); upgrade != "" {
		t.Fatalf("current database reported %q", upgrade)
	}
	// An opener that inspected the released schema but finds it current in
	// the migration transaction, because another opener migrated first,
	// reports nothing.
	noErr(t, reopened.migrate(ctx, schemaReleased))
	if upgrade := reopened.SchemaUpgrade(); upgrade != "" {
		t.Fatalf("already migrated database reported %q", upgrade)
	}
}
