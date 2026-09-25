package state

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Every released schema opens at the current schema, and a rebuilt table keeps
// every row that refers to it. Schema 14 (1.0.0 to 1.0.2) is upgraded in place
// to schema 15, which admits closed pull requests.
func TestReleasedSchemasUpgradeAndKeepPullRequestHistory(t *testing.T) {
	var released []int
	for _, step := range schemaSteps {
		if step.released {
			released = append(released, step.version)
		}
	}
	if len(released) == 0 || released[0] != 14 {
		t.Fatalf("released schemas=%v", released)
	}
	for _, version := range released {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			createReleasedSchemaWithPullRequest(t, directory, version)
			store := openUpgradedSchema(t, directory, currentSchemaVersion())
			defer store.Close()
			if fingerprint, objects, err := schemaFingerprint(context.Background(), store.db); err != nil || fingerprint != currentSchemaFingerprint || objects != currentSchemaObjects {
				t.Fatalf("upgraded schema fingerprint=%s objects=%d err=%v", fingerprint, objects, err)
			}
			assertPullRequestHistoryKept(t, store)
		})
	}
}

// The chain is table-driven: a test-only step 16 makes schema 14 two steps
// behind, and it upgrades through schema 15 and 16 in one Open. Schema 15
// becomes a released schema that upgrades one step, and the refusal messages
// follow the table.
func TestSchemaChainRunsEveryLaterStep(t *testing.T) {
	original := schemaSteps
	schemaSteps = append(slices.Clone(original), schemaStep{version: 16, statements: []string{
		`CREATE TABLE synthetic_step(title TEXT NOT NULL)`,
		`INSERT INTO synthetic_step(title) SELECT title FROM pull_requests`,
	}})
	t.Cleanup(func() { schemaSteps = original })
	ctx := context.Background()

	for _, version := range []int{14, 15} {
		t.Run("upgrade from "+strconv.Itoa(version), func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			createReleasedSchemaWithPullRequest(t, directory, version)
			store := openUpgradedSchema(t, directory, 16)
			defer store.Close()
			if upgrade, want := store.SchemaUpgrade(), "state database upgraded from schema "+strconv.Itoa(version)+" to 16"; upgrade != want {
				t.Fatalf("upgrade reported %q, want %q", upgrade, want)
			}
			var copied int
			noErr(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM synthetic_step WHERE title='Kept'`).Scan(&copied))
			if copied != 1 {
				t.Fatalf("synthetic step copied %d rows", copied)
			}
			// Closing needs the schema 15 constraint, so step 15 ran too.
			assertPullRequestHistoryKept(t, store)
		})
	}

	t.Run("refusals", func(t *testing.T) {
		for version, want := range map[int]string{
			13: "state database uses the unreleased development schema 13; this build upgrades only the committed baseline (no schema version) and released schemas 14 and 15, and opens schema 16",
			17: "state database schema version 17 is newer than this OwnGit build supports (16)",
		} {
			directory := filepath.Join(t.TempDir(), "state")
			createNumberedSchemaDatabase(t, directory, version)
			before := captureSchemaDirectory(t, directory)
			store, err := Open(ctx, directory)
			if store != nil {
				_ = store.Close()
			}
			if err == nil || err.Error() != want {
				t.Fatalf("schema %d error=%v", version, err)
			}
			assertSchemaDirectoryUnchanged(t, directory, before)
		}
	})

	// An opener that inspected schema 14 refuses a database that another
	// opener left at schema 15 instead of migrating it under the wrong start.
	t.Run("changed released version", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "state")
		createMigratedSchemaDatabase(t, directory, 15)
		db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
		defer db.Close()
		migrating := &Store{db: db, dir: directory}
		if err := migrating.migrate(ctx, schemaReleased(14)); !errors.Is(err, ErrInspectionUnstable) {
			t.Fatalf("changed released version error=%v", err)
		}
		if version, _, err := readSchemaVersion(ctx, db); err != nil || version != 15 {
			t.Fatalf("refused migration left schema %d err=%v", version, err)
		}
	})
}

// The chain starts at the baseline upgrade step, has no gap, and ends at the
// schema this build writes. 1.1.0 writes the same schema 15 as 1.0.3.
func TestSchemaStepsAreConsecutive(t *testing.T) {
	for index, step := range schemaSteps {
		if step.version != 6+index || len(step.statements) == 0 {
			t.Fatalf("step %d has version %d and %d statements", index, step.version, len(step.statements))
		}
	}
	if currentSchemaVersion() != 15 {
		t.Fatalf("current schema=%d", currentSchemaVersion())
	}
}

func createReleasedSchemaWithPullRequest(t *testing.T, directory string, version int) {
	t.Helper()
	createMigratedSchemaDatabase(t, directory, version)
	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	defer db.Close()
	source, target := "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	for _, statement := range []string{
		`INSERT INTO repositories(id,name,description,created_at) VALUES('project','project','',1)`,
		`INSERT INTO pull_requests(repository_id,number,title,source_branch,target_branch,status,created_at,updated_at) VALUES('project',1,'Kept','feature','main','open',1,2)`,
		`INSERT INTO pull_request_revisions(repository_id,pull_request_number,source_oid,target_oid,recorded_at) VALUES('project',1,'` + source + `','` + target + `',1)`,
		`INSERT INTO pull_request_reviews(repository_id,pull_request_number,sequence,source_oid,target_oid,status,reviewer_label,provenance,created_at) VALUES('project',1,1,'` + source + `','` + target + `','not_requested','','default',1)`,
		`INSERT INTO pull_request_merge_intents(repository_id,pull_request_number,source_oid,target_oid,mode,tree_oid,result_oid,receipt_ref,status,created_at,updated_at) VALUES('project',1,'` + source + `','` + target + `','','','','refs/owngit/pull-requests/1/merge-receipt','preparing',1,1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func openUpgradedSchema(t *testing.T, directory string, want int) *Store {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("open released schema: %v", err)
	}
	if version, err := store.schemaVersion(ctx); err != nil || version != want {
		store.Close()
		t.Fatalf("upgraded schema version=%d err=%v", version, err)
	}
	return store
}

// assertPullRequestHistoryKept checks the rows createReleasedSchemaWithPullRequest
// wrote, closes the pull request, and deletes the repository through the
// cascading foreign keys.
func assertPullRequestHistoryKept(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
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
	createMigratedSchemaDatabase(t, directory, 14)
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
	noErr(t, reopened.migrate(ctx, schemaReleased(14)))
	if upgrade := reopened.SchemaUpgrade(); upgrade != "" {
		t.Fatalf("already migrated database reported %q", upgrade)
	}
}
