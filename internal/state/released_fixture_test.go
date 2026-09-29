package state

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Databases that released builds wrote upgrade once to the current schema,
// end at exactly the catalog of a new database and keep every row. The
// fixtures are SQL dumps of stopped states made from the shared synthetic
// test data (testdata/released/README.md says how), so their catalogs are the
// text those releases stored, not text rebuilt from the schema steps.
func TestReleasedDatabasesUpgradeOnce(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		file    string
		version int
	}{
		{file: "schema14-1.0.1.sql", version: 14},
		{file: "schema14-1.0.2.sql", version: 14},
		{file: "schema15-1.0.3.sql", version: 15},
		{file: "schema15-1.0.3-upgraded-from-1.0.2.sql", version: 15},
		{file: "schema15-baseline-upgraded-by-1.0.3.sql", version: 15},
		{file: "schema15-1.1.2.sql", version: 15},
		{file: "schema15-1.1.2-upgraded-from-1.0.2.sql", version: 15},
	} {
		t.Run(test.file, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			loadReleasedDump(t, directory, test.file)
			db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
			before := tableRowCounts(t, db)
			noErr(t, db.Close())
			if before["repositories"] == 0 || before["pull_requests"] == 0 || before["pull_request_reviews"] == 0 {
				t.Fatalf("fixture has too few records: %v", before)
			}

			store, err := Open(ctx, directory)
			if err != nil {
				t.Fatalf("open released schema %d: %v", test.version, err)
			}
			want := "state database upgraded from schema " + strconv.Itoa(test.version) + " to " + strconv.Itoa(currentSchemaVersion())
			if upgrade := store.SchemaUpgrade(); upgrade != want {
				store.Close()
				t.Fatalf("upgrade reported %q, want %q", upgrade, want)
			}
			if fingerprint, objects, err := schemaFingerprint(ctx, store.db); err != nil || fingerprint != currentSchemaFingerprint || objects != currentSchemaObjects {
				store.Close()
				t.Fatalf("upgraded schema fingerprint=%s objects=%d err=%v", fingerprint, objects, err)
			}
			after := tableRowCounts(t, store.db)
			for table, count := range before {
				if after[table] != count {
					store.Close()
					t.Fatalf("table %s has %d rows after the upgrade, %d before", table, after[table], count)
				}
			}
			if _, err := store.RecoverySnapshot(ctx); err != nil {
				store.Close()
				t.Fatalf("snapshot of the upgraded state: %v", err)
			}
			noErr(t, store.Close())

			reopened, err := Open(ctx, directory)
			noErr(t, err)
			defer reopened.Close()
			if upgrade := reopened.SchemaUpgrade(); upgrade != "" {
				t.Fatalf("second open reported %q", upgrade)
			}
		})
	}
}

// A released database whose tables someone changed is refused before
// anything changes, whether an object was added or one was altered.
func TestAlteredReleasedDatabaseIsRefusedUnchanged(t *testing.T) {
	ctx := context.Background()
	for name, statement := range map[string]string{
		"added table":  `CREATE TABLE notes_by_hand(text TEXT)`,
		"added column": `ALTER TABLE repositories ADD COLUMN colour TEXT`,
		"added index":  `CREATE INDEX pull_requests_title ON pull_requests(title)`,
	} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			loadReleasedDump(t, directory, "schema15-1.1.2.sql")
			db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
			_, err := db.Exec(statement)
			noErr(t, err)
			noErr(t, db.Close())
			before := captureSchemaDirectory(t, directory)
			store, err := Open(ctx, directory)
			if store != nil {
				_ = store.Close()
			}
			want := "state database says schema 15 but its tables differ from the ones OwnGit wrote at schema 15, so this build does not upgrade it; undo the change to its tables, or restore a backup of the state with the OwnGit version that made the backup"
			if err == nil || err.Error() != want {
				t.Fatalf("altered schema error=%v", err)
			}
			assertSchemaDirectoryUnchanged(t, directory, before)
		})
	}
}

func loadReleasedDump(t *testing.T, directory, name string) {
	t.Helper()
	dump, err := os.ReadFile(filepath.Join("testdata", "released", name))
	noErr(t, err)
	db := createSchemaDatabase(t, directory)
	defer db.Close()
	if _, err := db.Exec(string(dump)); err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
}

func tableRowCounts(t *testing.T, db queryRower) map[string]int {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT GLOB 'sqlite_*'`)
	noErr(t, err)
	var tables []string
	for rows.Next() {
		var name string
		noErr(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	noErr(t, closeRows(rows))
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		noErr(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+table+`"`).Scan(&count))
		counts[table] = count
	}
	return counts
}
