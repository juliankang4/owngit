// Package statetest builds state directories for tests in other packages.
//
// It imports internal/state, so the state package's own tests cannot use it
// (they import internal/testfixture, which state must not import back) and
// keep their own copy of the same helper.
package statetest

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"owngit/internal/state"
)

// emptyStateSchema is the empty current-schema database that ordinary
// fixtures copy, generated once in this test binary.
var emptyStateSchema struct {
	once  sync.Once
	files map[string][]byte
	err   error
}

// CopiedStateDirectory creates directory as a private state directory that
// holds a copy of the current empty state database, for state.Open. Every
// fixture gets its own writable database: no database is shared, hard-linked
// or reused, and no released dump is used as the current schema. Tests of
// creation, migration, preflight, publication and permissions keep opening
// real new states.
func CopiedStateDirectory(directory string) (string, error) {
	emptyStateSchema.once.Do(func() { emptyStateSchema.files, emptyStateSchema.err = buildEmptyStateSchema() })
	if emptyStateSchema.err != nil {
		return "", emptyStateSchema.err
	}
	held, err := state.CreateDirectory(directory)
	if err != nil {
		return "", err
	}
	if err := held.Close(); err != nil {
		return "", err
	}
	for name, content := range emptyStateSchema.files {
		file, err := state.CreatePrivateFile(filepath.Join(directory, name))
		if err != nil {
			return "", err
		}
		if _, err := file.Write(content); err != nil {
			_ = file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
	}
	return directory, nil
}

// buildEmptyStateSchema opens a new state, closes it and returns the files
// it left behind.
func buildEmptyStateSchema() (map[string][]byte, error) {
	directory, err := os.MkdirTemp("", "owngit-empty-state-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	store, err := state.Open(context.Background(), directory)
	if err != nil {
		return nil, err
	}
	if err := store.Close(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = content
	}
	return files, nil
}
