//go:build windows

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func assertStateStoragePrivate(t *testing.T, directory string) {
	t.Helper()
	user, _, err := processIdentity()
	if err != nil {
		t.Fatal(err)
	}
	paths := []struct {
		path      string
		directory bool
	}{
		{path: directory, directory: true},
		{path: filepath.Join(directory, databaseName)},
		{path: filepath.Join(directory, databaseName+"-wal")},
		{path: filepath.Join(directory, databaseName+"-shm")},
	}
	for _, item := range paths {
		if _, err := os.Stat(item.path); os.IsNotExist(err) && !item.directory && item.path != filepath.Join(directory, databaseName) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		if err := validateOwnerOnly(item.path, user, item.directory); err != nil {
			t.Fatalf("state path %s is not protected for its current user owner: %v", item.path, err)
		}
	}
}
