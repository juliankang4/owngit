package testfixture

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func LoadReleasedState(t *testing.T, directory, name string) {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate released state fixtures")
	}
	dump, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "state", "testdata", "released", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteURI(filepath.Join(directory, "owngit.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(string(dump)); err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
}
