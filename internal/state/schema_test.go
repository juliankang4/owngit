package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/testfixture"
)

func TestUnsupportedSchemaPreservesDirectoryMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode probe; Windows ACL has native coverage")
	}
	ctx := context.Background()
	directory := t.TempDir()
	createNumberedSchemaDatabase(t, directory, 5)
	if err := os.Chmod(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	before := captureSchemaDirectory(t, directory)
	store, err := Open(ctx, directory)
	if store != nil {
		_ = store.Close()
	}
	if err == nil {
		t.Fatal("unsupported schema accepted")
	}
	info, statErr := os.Stat(directory)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("unsupported refusal changed directory permissions to %04o", info.Mode().Perm())
	}
	assertSchemaDirectoryUnchanged(t, directory, before)
}

func TestUnsupportedSchemaInCommittedWALPreservesSource(t *testing.T) {
	const fixtureVariable = "OWNGIT_UNSUPPORTED_WAL_FIXTURE"
	if directory := os.Getenv(fixtureVariable); directory != "" {
		db, err := sql.Open("sqlite", sqliteFileURI(filepath.Join(directory, databaseName)))
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range []string{
			`PRAGMA journal_mode=WAL`,
			`PRAGMA wal_autocheckpoint=0`,
			`CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL)`,
			`INSERT INTO metadata VALUES('schema_version','5')`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		os.Exit(73)
	}

	directory := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=^TestUnsupportedSchemaInCommittedWALPreservesSource$")
	child.Env = append(os.Environ(), fixtureVariable+"="+directory)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("fixture child did not retain its committed WAL: %v %s", err, output)
	}
	before := captureSchemaDirectory(t, directory)
	if wal, exists := before[databaseName+"-wal"]; !exists || len(wal.data) == 0 {
		t.Fatal("fixture did not retain a committed WAL")
	}
	store, err := Open(context.Background(), directory)
	if store != nil {
		_ = store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "schema 5") {
		t.Fatalf("unsupported WAL schema error=%v", err)
	}
	assertSchemaDirectoryUnchanged(t, directory, before)
}

const (
	// legacySchemaEightFingerprint is the catalog of the genuine database
	// emitted before the built-in review removal. It is also the classification
	// authority for a real schema 8 database.
	legacySchemaEightFingerprint = schemaEightFingerprint
	legacySchemaEightObjects     = schemaEightObjects
)

func TestFreshSchemaOpen(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{name: "new path"},
		{name: "existing empty database", prepare: createEmptySQLiteDatabase},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			if test.prepare != nil {
				test.prepare(t, directory)
			}
			store, err := Open(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			version, err := store.schemaVersion(ctx)
			if err != nil || version != currentSchemaVersion {
				store.Close()
				t.Fatalf("schema version=%d err=%v", version, err)
			}
			for _, table := range []string{"check_policies", "check_jobs", "check_runner_credentials", "check_observations"} {
				var count int
				if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
					store.Close()
					t.Fatal(err)
				}
				if count != 1 {
					store.Close()
					t.Fatalf("current schema table %q count=%d", table, count)
				}
			}
			for _, table := range []string{"import_sources", "import_runs", "import_ref_observations", "import_publication_intents", "import_stagings", "import_schedules", "import_initial_destinations"} {
				var count int
				if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
					store.Close()
					t.Fatal(err)
				}
				if count != 1 {
					store.Close()
					t.Fatalf("import schema table %q count=%d", table, count)
				}
			}
			var rawTable, expiryIndex int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='check_raw_logs'`).Scan(&rawTable); err != nil {
				store.Close()
				t.Fatal(err)
			}
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='check_raw_logs_expiry'`).Scan(&expiryIndex); err != nil {
				store.Close()
				t.Fatal(err)
			}
			if rawTable != 1 || expiryIndex != 1 {
				store.Close()
				t.Fatalf("raw log schema table=%d index=%d", rawTable, expiryIndex)
			}
			fingerprint, objects, err := schemaFingerprint(ctx, store.db)
			if err != nil || fingerprint == schemaEightFingerprint || fingerprint == schemaNineFingerprint || fingerprint == schemaTenFingerprint || objects <= schemaTenObjects {
				store.Close()
				t.Fatalf("fresh schema fingerprint=%s objects=%d err=%v", fingerprint, objects, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(ctx, directory)
			if err != nil {
				t.Fatalf("reopen current schema: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestKnownSchemaSixUpgradesToCurrentInPlace(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	buildGenuineSchemaSix(t, directory)
	addSchemaTestRepository(t, directory, "schema-six", "Schema Six", time.Unix(1_800_000_000, 0))

	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("migrate schema 6: %v", err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("migrated schema version=%d err=%v", version, err)
	}
	if repository, exists, err := store.Repository(ctx, "schema-six"); err != nil || !exists || repository.Name != "Schema Six" {
		t.Fatalf("preserved repository=%+v exists=%v err=%v", repository, exists, err)
	}
	var rawTable int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='check_raw_logs'`).Scan(&rawTable); err != nil || rawTable != 1 {
		t.Fatalf("raw log table count=%d err=%v", rawTable, err)
	}
}

func TestKnownSchemaSevenUpgradesToCurrentInPlace(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	buildGenuineSchemaSeven(t, directory)
	addSchemaTestRepository(t, directory, "schema-seven", "Schema Seven", time.Unix(1_800_000_001, 0))

	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("migrate schema 7: %v", err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("migrated schema version=%d err=%v", version, err)
	}
	if repository, exists, err := store.Repository(ctx, "schema-seven"); err != nil || !exists || repository.Name != "Schema Seven" {
		t.Fatalf("preserved repository=%+v exists=%v err=%v", repository, exists, err)
	}
	var directTables int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name GLOB 'direct_review_*'`).Scan(&directTables); err != nil || directTables != 5 {
		t.Fatalf("direct review tables=%d err=%v", directTables, err)
	}
}

func TestKnownSchemaEightUpgradesToCurrentInPlace(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	buildGenuineSchemaEight(t, directory)
	addSchemaTestRepository(t, directory, "schema-eight", "Schema Eight", time.Unix(1_800_000_002, 0))

	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("migrate schema 8: %v", err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("migrated schema version=%d err=%v", version, err)
	}
	if repository, exists, err := store.Repository(ctx, "schema-eight"); err != nil || !exists || repository.Name != "Schema Eight" {
		t.Fatalf("preserved repository=%+v exists=%v err=%v", repository, exists, err)
	}
	var directTables int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name GLOB 'direct_review_*'`).Scan(&directTables); err != nil || directTables != 5 {
		t.Fatalf("direct review tables=%d err=%v", directTables, err)
	}
}

// TestGenuineSchemaEightCatalogClassifiesAndMigrates builds the checked-in
// catalog extracted read-only from the genuine pre-removal database. It proves
// classification happens before any migration statement runs and that the
// migration preserves the original tables and rows.
func TestGenuineSchemaNineCatalogClassifiesAndMigrates(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	buildGenuineSchemaNine(t, directory)

	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	class, err := classifySchema(ctx, db)
	if err != nil || class != schemaNine {
		db.Close()
		t.Fatalf("genuine schema 9 class=%v err=%v", class, err)
	}
	fingerprint, objects, err := schemaFingerprint(ctx, db)
	if err != nil || fingerprint != schemaNineFingerprint || objects != schemaNineObjects {
		db.Close()
		t.Fatalf("genuine schema 9 fingerprint=%s objects=%d err=%v", fingerprint, objects, err)
	}
	if _, err := db.Exec(`INSERT INTO repositories(id,name,description,created_at) VALUES('genuine9','Genuine 9','',1)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("migrate genuine schema 9: %v", err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("migrated schema version=%d err=%v", version, err)
	}
	if repository, exists, err := store.Repository(ctx, "genuine9"); err != nil || !exists || repository.Name != "Genuine 9" {
		t.Fatalf("preserved repository=%+v exists=%v err=%v", repository, exists, err)
	}
	for table, column := range map[string]string{"check_policies": "execution_json", "check_jobs": "execution_json", "check_runner_credentials": "role"} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count); err != nil || count != 1 {
			t.Fatalf("schema 10 column %s.%s count=%d err=%v", table, column, count, err)
		}
	}
	var runtimeTables int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='check_job_runtime_ownership'`).Scan(&runtimeTables); err != nil || runtimeTables != 1 {
		t.Fatalf("schema 10 runtime ownership table count=%d err=%v", runtimeTables, err)
	}
	policy, err := store.SetCheckPolicy(ctx, CheckPolicyInput{
		RepositoryID: "genuine9", Executor: CheckExecutorHost, AllowedEvents: []string{"push"},
		MaxTimeoutMS: 60000, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
	}, time.Unix(2, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantCheckConsent(ctx, "genuine9", time.Unix(3, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	job, deduped, err := store.AdmitCheckJob(ctx, CheckJobRequest{
		RepositoryID: "genuine9", Trigger: "push", EventKey: "refs/heads/main@" + strings.Repeat("a", 40),
		SourceOID: strings.Repeat("a", 40), TriggerRef: "main",
		WorkflowDigest: strings.Repeat("c", 64), Checks: []CheckDefinition{{Name: "unit", Command: "true"}},
	}, time.Unix(4, 0).UTC())
	if err != nil || deduped || job.PolicyVersion != policy.Version {
		t.Fatalf("admit push after schema 9 migration job=%+v deduped=%v err=%v", job, deduped, err)
	}
}

func TestGenuineSchemaTenCatalogClassifiesAndMigrates(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	buildGenuineSchemaTen(t, directory)

	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	class, err := classifySchema(ctx, db)
	if err != nil || class != schemaTen {
		db.Close()
		t.Fatalf("genuine schema 10 class=%v err=%v", class, err)
	}
	fingerprint, objects, err := schemaFingerprint(ctx, db)
	if err != nil || fingerprint != schemaTenFingerprint || objects != schemaTenObjects {
		db.Close()
		t.Fatalf("genuine schema 10 fingerprint=%s objects=%d err=%v", fingerprint, objects, err)
	}
	if _, err := db.Exec(`INSERT INTO repositories(id,name,description,created_at) VALUES('genuine10','Genuine 10','',1)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("migrate genuine schema 10: %v", err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("migrated schema version=%d err=%v", version, err)
	}
	if repository, exists, err := store.Repository(ctx, "genuine10"); err != nil || !exists || repository.Name != "Genuine 10" {
		t.Fatalf("preserved repository=%+v exists=%v err=%v", repository, exists, err)
	}
	// The import tables must exist and be usable after the upgrade.
	source, err := store.ConfigureImportSource(ctx, ImportSourceInput{
		RepositoryID: "genuine10", URL: "https://example.invalid/team/project.git",
		Mode: ImportModeStandalone, Now: time.Unix(2, 0).UTC(),
	})
	if err != nil || source.SourceGeneration != 1 {
		t.Fatalf("configure import source after schema 10 migration source=%+v err=%v", source, err)
	}
	if _, err := store.SetImportSchedule(ctx, "genuine10", true, 5*time.Minute, time.Unix(3, 0).UTC()); err != nil {
		t.Fatalf("schedule import after schema 10 migration: %v", err)
	}
}

func TestForgedSchemaTenCatalogIsRefusedWithoutWrites(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	buildGenuineSchemaTen(t, directory)
	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	if _, err := db.Exec(`CREATE TABLE forged(value TEXT)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := captureSchemaDirectory(t, directory)
	store, err := Open(context.Background(), directory)
	if store != nil {
		_ = store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "does not match the supported schema 10 catalog") {
		t.Fatalf("forged schema 10 error=%v", err)
	}
	assertSchemaDirectoryUnchanged(t, directory, before)
}

func TestGenuineSchemaEightCatalogClassifiesAndMigrates(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	buildGenuineSchemaEight(t, directory)

	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	class, err := classifySchema(ctx, db)
	if err != nil || class != schemaEight {
		db.Close()
		t.Fatalf("genuine schema 8 class=%v err=%v", class, err)
	}
	fingerprint, objects, err := schemaFingerprint(ctx, db)
	if err != nil || fingerprint != schemaEightFingerprint || objects != schemaEightObjects {
		db.Close()
		t.Fatalf("genuine schema 8 fingerprint=%s objects=%d err=%v", fingerprint, objects, err)
	}
	if _, err := db.Exec(`INSERT INTO repositories(id,name,description,created_at) VALUES('genuine','Genuine','',1)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("migrate genuine schema 8: %v", err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("migrated schema version=%d err=%v", version, err)
	}
	if repository, exists, err := store.Repository(ctx, "genuine"); err != nil || !exists || repository.Name != "Genuine" {
		t.Fatalf("preserved repository=%+v exists=%v err=%v", repository, exists, err)
	}
	var directTables, jobColumn int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name GLOB 'direct_review_*'`).Scan(&directTables); err != nil || directTables != 5 {
		t.Fatalf("direct review tables=%d err=%v", directTables, err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('check_attempts') WHERE name='job_id'`).Scan(&jobColumn); err != nil || jobColumn != 1 {
		t.Fatalf("check attempt job column=%d err=%v", jobColumn, err)
	}
}

func TestForgedSchemaNineCatalogIsRefusedWithoutWrites(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	buildGenuineSchemaNine(t, directory)
	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	if _, err := db.Exec(`CREATE TABLE forged(value TEXT)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := captureSchemaDirectory(t, directory)
	store, err := Open(context.Background(), directory)
	if store != nil {
		_ = store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "does not match the supported schema 9 catalog") {
		t.Fatalf("forged schema 9 error=%v", err)
	}
	assertSchemaDirectoryUnchanged(t, directory, before)
}

func TestForgedSchemaEightCatalogIsRefusedWithoutWrites(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	buildGenuineSchemaEight(t, directory)
	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	if _, err := db.Exec(`CREATE TABLE forged(value TEXT)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := captureSchemaDirectory(t, directory)
	store, err := Open(context.Background(), directory)
	if store != nil {
		_ = store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "does not match the supported schema 8 catalog") {
		t.Fatalf("forged schema 8 error=%v", err)
	}
	assertSchemaDirectoryUnchanged(t, directory, before)
}

func TestSchemaEightRelabeledAsPredecessorIsRefusedWithoutWrites(t *testing.T) {
	for _, version := range []int{6, 7, 8} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			store, err := Open(context.Background(), directory)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
			if _, err := db.Exec(`UPDATE metadata SET value=? WHERE key='schema_version'`, version); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := captureSchemaDirectory(t, directory)
			opened, err := Open(context.Background(), directory)
			if opened != nil {
				_ = opened.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "does not match the supported schema") {
				t.Fatalf("relabeled schema %d error=%v", version, err)
			}
			assertSchemaDirectoryUnchanged(t, directory, before)
		})
	}
}

func TestForgedSchemaSixIsRefusedWithoutWrites(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	createNumberedSchemaDatabase(t, directory, 6)
	before := captureSchemaDirectory(t, directory)
	store, err := Open(context.Background(), directory)
	if store != nil {
		_ = store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "does not match the supported schema 6 catalog") {
		t.Fatalf("forged schema 6 error=%v", err)
	}
	assertSchemaDirectoryUnchanged(t, directory, before)
}

func TestSchemaTwelveAddsStructuredHEADOwnership(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	db := createSchemaDatabase(t, directory)
	for _, version := range []int{6, 7, 8, 9, 10, 11} {
		for _, statement := range migrations[version] {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema %d fixture: %v", version, err)
			}
		}
		if _, err := db.Exec(`INSERT INTO metadata(key,value) VALUES('schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, version); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	var notNull int
	var defaultValue sql.NullString
	if err := store.db.QueryRowContext(ctx, `SELECT "notnull",dflt_value FROM pragma_table_info('import_publication_intents') WHERE name='head_owned'`).Scan(&notNull, &defaultValue); err != nil || notNull != 1 || !defaultValue.Valid || defaultValue.String != "0" {
		t.Fatalf("head_owned notnull=%d default=%+v err=%v", notNull, defaultValue, err)
	}
}

func TestSchemaTwelveCatalogUpgradesWithoutInventingDestinationOwnership(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	db := createSchemaDatabase(t, directory)
	for _, version := range []int{6, 7, 8, 9, 10, 11, 12} {
		for _, statement := range migrations[version] {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema %d fixture: %v", version, err)
			}
		}
		if _, err := db.Exec(`INSERT INTO metadata(key,value) VALUES('schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, version); err != nil {
			t.Fatal(err)
		}
	}
	assertSchemaFingerprint(t, db, schemaTwelveFingerprint, 12)
	objects := 0
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name NOT GLOB 'sqlite_*'`).Scan(&objects); err != nil || objects != schemaTwelveObjects {
		t.Fatalf("schema 12 objects=%d err=%v", objects, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	if count, err := store.TableRowCount(ctx, "import_initial_destinations"); err != nil || count != 0 {
		t.Fatalf("upgraded ownership rows=%d err=%v", count, err)
	}
}

// The exact schema 13 catalog upgrades in place. The intent table rebuild
// keeps every row, rowid, and column, and then admits owner_resolved.
func TestSchemaThirteenCatalogUpgradesToOwnerResolution(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	db := createSchemaDatabase(t, directory)
	for _, version := range []int{6, 7, 8, 9, 10, 11, 12, 13} {
		for _, statement := range migrations[version] {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("apply schema %d fixture: %v", version, err)
			}
		}
		if _, err := db.Exec(`INSERT INTO metadata(key,value) VALUES('schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, version); err != nil {
			t.Fatal(err)
		}
	}
	assertSchemaFingerprint(t, db, schemaThirteenFingerprint, 13)
	objects := 0
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name NOT GLOB 'sqlite_*'`).Scan(&objects); err != nil || objects != schemaThirteenObjects {
		t.Fatalf("schema 13 objects=%d err=%v", objects, err)
	}
	head := `{"HEAD":"symbolic refs/heads/main ` + strings.Repeat("a", 40) + `","refs/heads/main":"` + strings.Repeat("a", 40) + `"}`
	if _, err := db.Exec(`INSERT INTO import_publication_intents(rowid,id,repository_id,run_id,source_generation,authority_revision,status,
		expected_json,desired_json,observed_json,retained_json,reason,created_at,updated_at,head_owned)
		VALUES(7,?,'project',?,1,1,'complete',?,?,?,'{}','kept',1,2,1)`,
		strings.Repeat("b", 32), strings.Repeat("c", 32), head, head, head); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	var rowID, headOwned int
	var status, reason, desired string
	if err := store.db.QueryRowContext(ctx, `SELECT rowid,status,reason,desired_json,head_owned FROM import_publication_intents WHERE id=?`, strings.Repeat("b", 32)).
		Scan(&rowID, &status, &reason, &desired, &headOwned); err != nil {
		t.Fatal(err)
	}
	if rowID != 7 || status != ImportIntentComplete || reason != "kept" || desired != head || headOwned != 1 {
		t.Fatalf("upgraded intent rowid=%d status=%s reason=%s desired=%s head_owned=%d", rowID, status, reason, desired, headOwned)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE import_publication_intents SET status='owner_resolved',head_owned=0 WHERE id=?`, strings.Repeat("b", 32)); err != nil {
		t.Fatalf("upgraded table refuses owner_resolved: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE import_publication_intents SET status='bogus' WHERE id=?`, strings.Repeat("b", 32)); err == nil {
		t.Fatal("upgraded table lost its status constraint")
	}
	var indexes int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='import_publication_intents_repository'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("upgraded intent index count=%d err=%v", indexes, err)
	}
}

func TestSchemaMigrationRefusesAnIncompatibleOwnerChange(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "state")
	buildGenuineSchemaSix(t, directory)

	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	defer db.Close()
	migrating := &Store{db: db, dir: directory}
	if err := migrating.migrate(ctx, schemaBaseline); !errors.Is(err, ErrInspectionUnstable) {
		t.Fatalf("incompatible migration owner error=%v", err)
	}
	version, err := migrating.schemaVersion(ctx)
	if err != nil || version != 6 {
		t.Fatalf("refused migration version=%d err=%v", version, err)
	}
	var rawTable int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='check_raw_logs'`).Scan(&rawTable); err != nil || rawTable != 0 {
		t.Fatalf("refused migration raw table=%d err=%v", rawTable, err)
	}
}

func TestCommittedBaselineSchemaUpgradesInPlace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	directory := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	if err := testfixture.CreateCommittedBaselineState(ctx, directory, testfixture.BaselineStateOptions{
		RepositoryRoot: repositoryRoot, AdminPasswordHash: "synthetic-admin-hash",
	}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(directory, databaseName)
	db := openSchemaDatabase(t, path)
	var baselineReviewDDL string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='pull_request_reviews'`).Scan(&baselineReviewDDL); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if strings.Contains(baselineReviewDDL, ReviewNotRequested) || !strings.Contains(baselineReviewDDL, "'skipped'") {
		db.Close()
		t.Fatalf("fixture does not contain the review constraint from %s: %s", testfixture.CommittedBaselineStateCommit, baselineReviewDDL)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, directory)
	if err != nil {
		t.Fatalf("open committed baseline: %v", err)
	}
	defer store.Close()
	if version, err := store.schemaVersion(ctx); err != nil || version != currentSchemaVersion {
		t.Fatalf("upgraded schema version=%d err=%v", version, err)
	}
	settings, err := store.Settings(ctx)
	if err != nil || !settings.Initialized || settings.RepositoryRoot != repositoryRoot || settings.AccessMode != "open" || settings.AccessSessionVersion != 3 || settings.AdminSessionVersion != 4 || !settings.InsecureHTTPAccepted {
		t.Fatalf("upgraded settings=%+v err=%v", settings, err)
	}
	if hash, err := store.PasswordHash(ctx, "admin"); err != nil || hash != "synthetic-admin-hash" {
		t.Fatalf("upgraded administrator hash=%q err=%v", hash, err)
	}
	if repository, exists, err := store.Repository(ctx, testfixture.BaselineRepositoryID); err != nil || !exists || repository.Name != "Baseline project" {
		t.Fatalf("upgraded repository=%+v exists=%v err=%v", repository, exists, err)
	}
	if hosts, err := store.TrustedHosts(ctx); err != nil || len(hosts) != 1 || hosts[0] != "baseline.example.invalid" {
		t.Fatalf("upgraded trusted hosts=%v err=%v", hosts, err)
	}
	if session, exists, err := store.Session(ctx, testfixture.BaselineSessionToken, "admin", time.Unix(1_800_000_000, 0)); err != nil || !exists || session.CSRF != "baseline-csrf" || session.Version != 4 {
		t.Fatalf("upgraded session=%+v exists=%v err=%v", session, exists, err)
	}
	snapshot, err := store.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PullRequests) != 1 || len(snapshot.PullRequestRevisions) != 1 || len(snapshot.PullRequestReviews) != 1 {
		t.Fatalf("upgraded pull request history: requests=%d revisions=%d reviews=%d", len(snapshot.PullRequests), len(snapshot.PullRequestRevisions), len(snapshot.PullRequestReviews))
	}
	if review := snapshot.PullRequestReviews[0]; review.Status != ReviewApproved || review.SourceOID != testfixture.BaselineSourceOID || review.TargetOID != testfixture.BaselineTargetOID {
		t.Fatalf("upgraded review=%+v", review)
	}
	if _, err := store.CreateTask(ctx, testfixture.BaselineRepositoryID, "Upgraded task", time.Now()); err != nil {
		t.Fatalf("upgraded baseline cannot store tasks: %v", err)
	}
	if _, err := store.CreatePullRequest(ctx, testfixture.BaselineRepositoryID, "Optional review", "next", "main", strings.Repeat("c", 40), strings.Repeat("d", 40), ReviewNotRequested, time.Now()); err != nil {
		t.Fatalf("upgraded review constraint rejected an optional review: %v", err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(directory); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("state directory mode=%v err=%v", infoMode(info), err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("state database mode=%v err=%v", infoMode(info), err)
		}
	}
}

func TestUnsupportedNumberedSchemasAreRefusedWithoutWrites(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{1, 2, 3, 4, 5, 99} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			createNumberedSchemaDatabase(t, directory, version)
			before := captureSchemaDirectory(t, directory)
			store, err := Open(ctx, directory)
			if store != nil {
				_ = store.Close()
			}
			if version > currentSchemaVersion {
				if err == nil || !strings.Contains(err.Error(), "newer") {
					t.Fatalf("schema %d error=%v", version, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "unreleased development schema") {
				t.Fatalf("schema %d error=%v", version, err)
			}
			assertSchemaDirectoryUnchanged(t, directory, before)
		})
	}
}

func TestUnversionedNonBaselineSchemasAreRefusedWithoutWrites(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{name: "unrelated table", prepare: func(t *testing.T, directory string) {
			db := createSchemaDatabase(t, directory)
			if _, err := db.Exec(`CREATE TABLE unrelated(value TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "partial baseline", prepare: func(t *testing.T, directory string) {
			db := createSchemaDatabase(t, directory)
			if _, err := db.Exec(`CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "current schema with marker removed", prepare: func(t *testing.T, directory string) {
			store, err := Open(ctx, directory)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Exec(ctx, `DELETE FROM metadata WHERE key='schema_version'`); err != nil {
				store.Close()
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "state")
			test.prepare(t, directory)
			before := captureSchemaDirectory(t, directory)
			store, err := Open(ctx, directory)
			if store != nil {
				_ = store.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "does not match the committed baseline") {
				t.Fatalf("unversioned schema error=%v", err)
			}
			assertSchemaDirectoryUnchanged(t, directory, before)
		})
	}
}

type schemaTestExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

type genuineSchemaEightCatalog struct {
	SchemaVersion      int    `json:"schema_version"`
	CatalogFingerprint string `json:"catalog_fingerprint"`
	Objects            []struct {
		Type string `json:"type"`
		Name string `json:"name"`
		SQL  string `json:"sql"`
	} `json:"objects"`
}

func buildGenuineSchemaNine(t *testing.T, directory string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "genuine-schema9.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if actual := hex.EncodeToString(digest[:]); actual != "ad9adf485581dff0d5a151651b2e4dcf850d4ba126c46837d8391f321c699afb" {
		t.Fatalf("genuine schema 9 public catalog SHA-256=%s", actual)
	}
	var catalog genuineSchemaEightCatalog
	if err := json.Unmarshal(content, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != 9 || catalog.CatalogFingerprint != schemaNineFingerprint || len(catalog.Objects) != schemaNineObjects {
		t.Fatalf("genuine schema 9 catalog shape version=%d fingerprint=%s objects=%d", catalog.SchemaVersion, catalog.CatalogFingerprint, len(catalog.Objects))
	}
	db := createSchemaDatabase(t, directory)
	defer db.Close()
	for _, objectType := range []string{"table", "index"} {
		for _, object := range catalog.Objects {
			if object.Type != objectType {
				continue
			}
			if _, err := db.Exec(object.SQL); err != nil {
				t.Fatalf("construct genuine schema 9 %s %q: %v", object.Type, object.Name, err)
			}
		}
	}
	if _, err := db.Exec(`INSERT INTO metadata(key,value) VALUES('schema_version','9')`); err != nil {
		t.Fatal(err)
	}
}

// buildGenuineSchemaTen reconstructs the authoritative schema 10 catalog that
// the accepted pre-import executable emitted. The fixture bytes are checked in
// unchanged and their SHA-256 is pinned.
func buildGenuineSchemaTen(t *testing.T, directory string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "genuine-schema10.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if actual := hex.EncodeToString(digest[:]); actual != "22198b6bf4027e2df361c47bd5bc0569d6672ff6d7a14e6034a0bd013b3f74f6" {
		t.Fatalf("genuine schema 10 public catalog SHA-256=%s", actual)
	}
	var catalog genuineSchemaEightCatalog
	if err := json.Unmarshal(content, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != 10 || catalog.CatalogFingerprint != schemaTenFingerprint || len(catalog.Objects) != schemaTenObjects {
		t.Fatalf("genuine schema 10 catalog shape version=%d fingerprint=%s objects=%d", catalog.SchemaVersion, catalog.CatalogFingerprint, len(catalog.Objects))
	}
	db := createSchemaDatabase(t, directory)
	defer db.Close()
	for _, objectType := range []string{"table", "index"} {
		for _, object := range catalog.Objects {
			if object.Type != objectType {
				continue
			}
			if _, err := db.Exec(object.SQL); err != nil {
				t.Fatalf("construct genuine schema 10 %s %q: %v", object.Type, object.Name, err)
			}
		}
	}
	if _, err := db.Exec(`INSERT INTO metadata(key,value) VALUES('schema_version','10')`); err != nil {
		t.Fatal(err)
	}
}

// buildGenuineSchemaEight reconstructs the genuine schema 8 catalog that was
// extracted read-only from the pre-removal database. The fixture bytes are
// checked in unchanged.
func buildGenuineSchemaEight(t *testing.T, directory string) {
	t.Helper()
	db := createSchemaDatabase(t, directory)
	defer db.Close()
	applyGenuineSchemaEight(t, db)
}

func applyGenuineSchemaEight(t *testing.T, execer schemaTestExecer) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", "genuine-schema8.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog genuineSchemaEightCatalog
	if err := json.Unmarshal(content, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.SchemaVersion != 8 || catalog.CatalogFingerprint != schemaEightFingerprint || len(catalog.Objects) != schemaEightObjects {
		t.Fatalf("genuine catalog shape version=%d fingerprint=%s objects=%d", catalog.SchemaVersion, catalog.CatalogFingerprint, len(catalog.Objects))
	}
	for _, objectType := range []string{"table", "index"} {
		for _, object := range catalog.Objects {
			if object.Type != objectType {
				continue
			}
			if _, err := execer.Exec(object.SQL); err != nil {
				t.Fatalf("construct genuine schema 8 %s %q: %v", object.Type, object.Name, err)
			}
		}
	}
	if _, err := execer.Exec(`INSERT INTO metadata(key,value) VALUES('schema_version','8')`); err != nil {
		t.Fatal(err)
	}
}

func removeSchemaEightObjects(t *testing.T, execer schemaTestExecer) {
	t.Helper()
	for _, statement := range []string{
		`DROP TABLE direct_review_requests`,
		`DROP TABLE direct_review_task_contexts`,
		`DROP TABLE direct_review_probes`,
		`DROP TABLE direct_review_repository_settings`,
		`DROP TABLE direct_review_credentials`,
		`DROP INDEX pull_request_reviews_event`,
		`ALTER TABLE pull_request_reviews DROP COLUMN review_event_id`,
		`UPDATE metadata SET value='7' WHERE key='schema_version'`,
	} {
		if _, err := execer.Exec(statement); err != nil {
			t.Fatalf("construct genuine schema 7 fixture with %q: %v", statement, err)
		}
	}
}

func buildGenuineSchemaSeven(t *testing.T, directory string) {
	t.Helper()
	buildGenuineSchemaEight(t, directory)
	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	defer db.Close()
	removeSchemaEightObjects(t, db)
	assertSchemaFingerprint(t, db, schemaSevenFingerprint, 7)
}

func buildGenuineSchemaSix(t *testing.T, directory string) {
	t.Helper()
	buildGenuineSchemaSeven(t, directory)
	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	defer db.Close()
	for _, statement := range []string{
		`DROP TABLE check_raw_logs`,
		`UPDATE metadata SET value='6' WHERE key='schema_version'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	assertSchemaFingerprint(t, db, schemaSixFingerprint, 6)
}

func addSchemaTestRepository(t *testing.T, directory, id, name string, createdAt time.Time) {
	t.Helper()
	db := openSchemaDatabase(t, filepath.Join(directory, databaseName))
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO repositories(id,name,description,created_at) VALUES(?,?,?,?)`, id, name, "", createdAt.Unix()); err != nil {
		t.Fatal(err)
	}
}
func assertSchemaFingerprint(t *testing.T, db queryRower, expected string, version int) {
	t.Helper()
	fingerprint, _, err := schemaFingerprint(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != expected {
		t.Fatalf("schema %d fixture fingerprint=%s, want accepted predecessor %s", version, fingerprint, expected)
	}
}

func createEmptySQLiteDatabase(t *testing.T, directory string) {
	t.Helper()
	db := createSchemaDatabase(t, directory)
	if _, err := db.Exec(`VACUUM`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func createNumberedSchemaDatabase(t *testing.T, directory string, version int) {
	t.Helper()
	db := createSchemaDatabase(t, directory)
	if _, err := db.Exec(`CREATE TABLE metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO metadata(key,value) VALUES('schema_version',?)`, version); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func createSchemaDatabase(t *testing.T, directory string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return openSchemaDatabase(t, filepath.Join(directory, databaseName))
}

func openSchemaDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

type schemaFileSnapshot struct {
	mode os.FileMode
	data []byte
}

func captureSchemaDirectory(t *testing.T, directory string) map[string]schemaFileSnapshot {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string]schemaFileSnapshot, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("unexpected schema fixture entry %q", entry.Name())
		}
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		snapshot[entry.Name()] = schemaFileSnapshot{mode: info.Mode(), data: content}
	}
	return snapshot
}

func assertSchemaDirectoryUnchanged(t *testing.T, directory string, before map[string]schemaFileSnapshot) {
	t.Helper()
	after := captureSchemaDirectory(t, directory)
	if len(after) != len(before) {
		t.Fatalf("refusal changed state directory entries: before=%v after=%v", mapKeys(before), mapKeys(after))
	}
	for name, want := range before {
		got, exists := after[name]
		if !exists || got.mode != want.mode || !bytes.Equal(got.data, want.data) {
			t.Fatalf("refusal changed %q", name)
		}
	}
}

func mapKeys(values map[string]schemaFileSnapshot) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
