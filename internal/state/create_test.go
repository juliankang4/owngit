package state

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestCreatingADatabaseNeverShowsARollbackJournal watches the state directory
// while Open creates a new database. The preflight refuses a rollback journal
// beside the database, so a journal visible under the final name, even for a
// moment, makes a concurrent opener fail and would block every later start if
// the creating process died at that point.
func TestCreatingADatabaseNeverShowsARollbackJournal(t *testing.T) {
	ctx := context.Background()
	for attempt := 0; attempt < 40; attempt++ {
		directory := filepath.Join(t.TempDir(), "state")
		journal := filepath.Join(directory, databaseName+journalSuffix)
		var seen atomic.Bool
		stop := make(chan struct{})
		var watcher sync.WaitGroup
		watcher.Add(1)
		go func() {
			defer watcher.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := os.Lstat(journal); err == nil {
					seen.Store(true)
				}
			}
		}()
		store, err := Open(ctx, directory)
		close(stop)
		watcher.Wait()
		noErr(t, err)
		noErr(t, store.Close())
		if seen.Load() {
			t.Fatalf("attempt %d: %s was visible while the database was created", attempt, databaseName+journalSuffix)
		}
	}
}

// TestCreatedDatabaseIsPublishedInWALModeWithoutReplacing checks the file that
// takes the final name and the path where another opener published first.
func TestCreatedDatabaseIsPublishedInWALModeWithoutReplacing(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	path := filepath.Join(directory, databaseName)
	noErr(t, createDatabase(ctx, path))
	header := make([]byte, 20)
	file, err := os.Open(path)
	noErr(t, err)
	_, err = io.ReadFull(file, header)
	noErr(t, errors.Join(err, file.Close()))
	if header[18] != 2 || header[19] != 2 {
		t.Fatalf("published database header read/write versions %d/%d, want WAL (2/2)", header[18], header[19])
	}
	first, err := os.Stat(path)
	noErr(t, err)
	noErr(t, createDatabase(ctx, path))
	second, err := os.Stat(path)
	noErr(t, err)
	if !os.SameFile(first, second) || !second.ModTime().Equal(first.ModTime()) {
		t.Fatal("a later creation replaced the published database")
	}
	entries, err := os.ReadDir(directory)
	noErr(t, err)
	if len(entries) != 1 {
		t.Fatalf("entries after two creations: %v, want only %s", entries, databaseName)
	}
}

// TestInterruptedCreationLeftoversDoNotBlockOpen places the entries that a
// crash during creation can leave, a temporary database with its rollback
// journal, and requires Open to create and open the database.
func TestInterruptedCreationLeftoversDoNotBlockOpen(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	noErr(t, os.Mkdir(directory, 0o700))
	leftover := filepath.Join(directory, databaseName+".new-0123456789abcdef")
	noErr(t, os.WriteFile(leftover, nil, 0o600))
	noErr(t, os.WriteFile(leftover+journalSuffix, []byte("partial journal"), 0o600))
	store, err := Open(context.Background(), directory)
	noErr(t, err)
	noErr(t, store.Close())
}

// TestConcurrentFirstOpensShareOneDatabase starts several openers on the same
// missing database. Each must either open it or report retryable instability,
// and none may see a rollback journal.
func TestConcurrentFirstOpensShareOneDatabase(t *testing.T) {
	ctx := context.Background()
	for attempt := 0; attempt < 10; attempt++ {
		directory := filepath.Join(t.TempDir(), "state")
		var wait sync.WaitGroup
		errs := make([]error, 6)
		for index := range errs {
			wait.Add(1)
			go func() {
				defer wait.Done()
				store, err := Open(ctx, directory)
				if err == nil {
					err = store.Close()
				}
				errs[index] = err
			}()
		}
		wait.Wait()
		for index, err := range errs {
			if err != nil && !errors.Is(err, ErrInspectionUnstable) {
				t.Fatalf("attempt %d opener %d: %v", attempt, index, err)
			}
		}
		store, err := Open(ctx, directory)
		noErr(t, err)
		version, err := store.schemaVersion(ctx)
		noErr(t, store.Close())
		noErr(t, err)
		if version != currentSchemaVersion() {
			t.Fatalf("attempt %d: schema version %d, want %d", attempt, version, currentSchemaVersion())
		}
	}
}
