//go:build !windows

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func assertStateStoragePrivate(t *testing.T, directory string) {
	t.Helper()
	paths := []string{
		directory,
		filepath.Join(directory, databaseName),
		filepath.Join(directory, databaseName+"-wal"),
		filepath.Join(directory, databaseName+"-shm"),
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if os.IsNotExist(err) && path != directory && path != filepath.Join(directory, databaseName) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got&0o077 != 0 {
			t.Fatalf("%s permissions are %o, want no group/other access", path, got)
		}
	}
}
