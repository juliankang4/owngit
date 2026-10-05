package statetest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/state"
)

// An ordinary fixture copies the current empty database instead of building
// it again, so the copy must be indistinguishable from a state this build
// created, and two fixtures in one binary must stay independent.
func TestCopiedStateDirectoryMatchesAFreshStateAndStaysIndependent(t *testing.T) {
	ctx := context.Background()
	freshDirectory := filepath.Join(t.TempDir(), "state")
	fresh, err := state.Open(ctx, freshDirectory)
	noErr(t, err)
	defer fresh.Close()

	copiedDirectory, err := CopiedStateDirectory(filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	copied, err := state.Open(ctx, copiedDirectory)
	noErr(t, err)
	defer copied.Close()

	copiedCatalog, freshCatalog := stateCatalog(t, copiedDirectory), stateCatalog(t, freshDirectory)
	if !reflect.DeepEqual(copiedCatalog, freshCatalog) {
		t.Fatalf("the copied state has catalog %v, a fresh one %v", copiedCatalog, freshCatalog)
	}
	copiedMetadata, freshMetadata := metadataRows(t, copiedDirectory), metadataRows(t, freshDirectory)
	if !reflect.DeepEqual(copiedMetadata, freshMetadata) {
		t.Fatalf("the copied state holds %v, a fresh one %v", copiedMetadata, freshMetadata)
	}
	copiedSettings, err := copied.Settings(ctx)
	noErr(t, err)
	freshSettings, err := fresh.Settings(ctx)
	noErr(t, err)
	if copiedSettings != freshSettings {
		t.Fatalf("the copied state defaults to %+v, a fresh one to %+v", copiedSettings, freshSettings)
	}
	copiedDirectoryInfo, err := os.Stat(copiedDirectory)
	noErr(t, err)
	freshDirectoryInfo, err := os.Stat(freshDirectory)
	noErr(t, err)
	if copiedDirectoryInfo.Mode().Perm() != freshDirectoryInfo.Mode().Perm() {
		t.Fatalf("the copied directory mode is %v, a fresh one %v", copiedDirectoryInfo.Mode(), freshDirectoryInfo.Mode())
	}
	copiedDatabaseInfo, err := os.Stat(filepath.Join(copiedDirectory, databaseName))
	noErr(t, err)
	freshDatabaseInfo, err := os.Stat(filepath.Join(freshDirectory, databaseName))
	noErr(t, err)
	if copiedDatabaseInfo.Mode().Perm() != freshDatabaseInfo.Mode().Perm() {
		t.Fatalf("the copied database mode is %v, a fresh one %v", copiedDatabaseInfo.Mode(), freshDatabaseInfo.Mode())
	}
	for _, directory := range []string{freshDirectory, copiedDirectory} {
		if err := state.ValidatePrivateFile(filepath.Join(directory, databaseName)); err != nil {
			t.Fatalf("the state database in %s is not private: %v", directory, err)
		}
	}

	// Every fixture is its own database.
	otherDirectory, err := CopiedStateDirectory(filepath.Join(t.TempDir(), "state"))
	noErr(t, err)
	other, err := state.Open(ctx, otherDirectory)
	noErr(t, err)
	defer other.Close()
	noErr(t, copied.Exec(ctx, `INSERT INTO metadata(key,value) VALUES('fixture-check','one')`))
	if rows := metadataRowCount(t, copiedDirectory, "fixture-check"); rows != 1 {
		t.Fatalf("the write in the copied fixture left %d rows", rows)
	}
	if rows := metadataRowCount(t, otherDirectory, "fixture-check"); rows != 0 {
		t.Fatal("a write in one copied fixture is visible in another")
	}
}

// databaseName is the file a state directory holds.
const databaseName = "owngit.sqlite"

// stateCatalog returns every entry of a state database's catalog, the same
// comparison internal/state's own test makes.
func stateCatalog(t *testing.T, directory string) []string {
	t.Helper()
	database := openStateDatabase(t, directory)
	defer database.Close()
	rows, err := database.QueryContext(context.Background(),
		`SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_master ORDER BY type,name,tbl_name,sql`)
	noErr(t, err)
	defer rows.Close()
	var catalog []string
	for rows.Next() {
		var objectType, name, table, statement string
		noErr(t, rows.Scan(&objectType, &name, &table, &statement))
		catalog = append(catalog, objectType+" "+name+" "+table+" "+strings.Join(strings.Fields(statement), " "))
	}
	noErr(t, rows.Err())
	return catalog
}

// metadataRows returns the metadata keys and values a state database holds.
func metadataRows(t *testing.T, directory string) map[string]string {
	t.Helper()
	database := openStateDatabase(t, directory)
	defer database.Close()
	rows, err := database.QueryContext(context.Background(), `SELECT key, value FROM metadata`)
	noErr(t, err)
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		noErr(t, rows.Scan(&key, &value))
		values[key] = value
	}
	noErr(t, rows.Err())
	return values
}

// metadataRowCount counts the metadata rows a key has.
func metadataRowCount(t *testing.T, directory, key string) int {
	t.Helper()
	database := openStateDatabase(t, directory)
	defer database.Close()
	var rows int
	noErr(t, database.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM metadata WHERE key=?`, key).Scan(&rows))
	return rows
}

// openStateDatabase opens the state directory's database for reading beside
// the store that holds it.
func openStateDatabase(t *testing.T, directory string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(directory, databaseName))
	noErr(t, err)
	return database
}

// noErr stops the test on an unexpected error. t.Helper keeps the failure
// line at the caller.
func noErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
