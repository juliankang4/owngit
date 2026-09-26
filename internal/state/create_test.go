package state

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
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
	assertPublishesOnceInWALMode(t)
}

// assertPublishesOnceInWALMode creates a database twice in a new directory.
// The first creation must publish a WAL database, the second must keep it and
// leave no temporary entry. extra names the other entries the directory may
// hold afterwards.
func assertPublishesOnceInWALMode(t *testing.T, extra ...string) {
	t.Helper()
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
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := append([]string{databaseName}, extra...)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("entries after two creations: %v, want %v", names, want)
	}
}

// usePublishers replaces the exclusive rename and the hard link for one test.
// A nil function keeps the real primitive.
func usePublishers(t *testing.T, renameExclusive, link func(oldPath, newPath string) error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows publishes with MoveFileEx only")
	}
	saved := publishers
	t.Cleanup(func() { publishers = saved })
	if renameExclusive != nil {
		publishers.renameExclusive = renameExclusive
	}
	if link != nil {
		publishers.link = link
	}
}

func failWith(err error) func(string, string) error {
	return func(string, string) error { return err }
}

// TestPublishFallsBackToAHardLink covers filesystems without an exclusive
// rename, which report one of these codes.
func TestPublishFallsBackToAHardLink(t *testing.T) {
	for _, code := range []syscall.Errno{syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.EINVAL, syscall.ENOSYS} {
		t.Run(code.Error(), func(t *testing.T) {
			links := 0
			usePublishers(t, failWith(code), func(oldPath, newPath string) error {
				links++
				return os.Link(oldPath, newPath)
			})
			assertPublishesOnceInWALMode(t)
			if links != 2 {
				t.Fatalf("hard link used %d times, want 2", links)
			}
		})
	}
}

// TestPublishFallsBackToALockedRename covers filesystems without an exclusive
// rename or hard links, such as FAT on Linux (EPERM) or exFAT on macOS.
func TestPublishFallsBackToALockedRename(t *testing.T) {
	for _, code := range []syscall.Errno{syscall.EPERM, syscall.ENOTSUP, syscall.EOPNOTSUPP} {
		t.Run(code.Error(), func(t *testing.T) {
			usePublishers(t, failWith(syscall.ENOTSUP), failWith(code))
			assertPublishesOnceInWALMode(t, createLockFile)
		})
	}
}

// TestPublishReportsOtherFailures keeps a real failure of a primitive from
// being mistaken for missing support.
func TestPublishReportsOtherFailures(t *testing.T) {
	for name, publishersUnderTest := range map[string][2]func(string, string) error{
		"exclusive rename": {failWith(syscall.EIO), nil},
		"hard link":        {failWith(syscall.ENOTSUP), failWith(syscall.EIO)},
	} {
		t.Run(name, func(t *testing.T) {
			usePublishers(t, publishersUnderTest[0], publishersUnderTest[1])
			directory := t.TempDir()
			if err := createDatabase(context.Background(), filepath.Join(directory, databaseName)); !errors.Is(err, syscall.EIO) {
				t.Fatalf("createDatabase: %v, want EIO", err)
			}
			entries, err := os.ReadDir(directory)
			noErr(t, err)
			if len(entries) != 0 {
				t.Fatalf("a failed creation left %v", entries)
			}
		})
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
	assertConcurrentFirstOpensShareOneDatabase(t)
}

// TestConcurrentFirstOpensShareOneDatabaseUnderTheCreationLock repeats the
// concurrent first opens where only the locked rename can publish.
func TestConcurrentFirstOpensShareOneDatabaseUnderTheCreationLock(t *testing.T) {
	usePublishers(t, failWith(syscall.ENOTSUP), failWith(syscall.EPERM))
	assertConcurrentFirstOpensShareOneDatabase(t)
}

func assertConcurrentFirstOpensShareOneDatabase(t *testing.T) {
	t.Helper()
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
