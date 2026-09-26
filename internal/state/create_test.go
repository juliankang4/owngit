package state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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
// leave no temporary entry. extra names the other entries the directory must
// hold afterwards. AppleDouble entries ("._*") that macOS adds on exFAT and
// FAT volumes are ignored, and so is a creation lock that extra does not
// require, so the check also runs on such volumes.
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
	want := append([]string{databaseName}, extra...)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "._") || name == createLockFile && !slices.Contains(want, name) {
			continue
		}
		names = append(names, name)
	}
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
	for _, code := range []syscall.Errno{syscall.ENOTSUP, syscall.EOPNOTSUPP, syscall.EINVAL, syscall.ENOSYS, syscall.EPERM} {
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
	// Older container seccomp profiles refuse renameat2 with EPERM.
	t.Run("exclusive rename refused with EPERM", func(t *testing.T) {
		usePublishers(t, failWith(syscall.EPERM), failWith(syscall.EPERM))
		assertPublishesOnceInWALMode(t, createLockFile)
	})
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

// TestUnremovableTemporaryNameDoesNotFailTheOpen makes removing the temporary
// name fail after the database was published. Open must still succeed.
func TestUnremovableTemporaryNameDoesNotFailTheOpen(t *testing.T) {
	refuseTemporaryRemoval := func(path string) error {
		if strings.Contains(filepath.Base(path), ".new-") {
			return &os.PathError{Op: "remove", Path: path, Err: syscall.EACCES}
		}
		return os.Remove(path)
	}
	t.Run("after the hard link", func(t *testing.T) {
		usePublishers(t, failWith(syscall.ENOTSUP), nil)
		publishers.remove = refuseTemporaryRemoval
		store, err := Open(context.Background(), filepath.Join(t.TempDir(), "state"))
		noErr(t, err)
		noErr(t, store.Close())
	})
	t.Run("after another opener published", func(t *testing.T) {
		usePublishers(t, failWith(syscall.EEXIST), nil)
		publishers.remove = refuseTemporaryRemoval
		noErr(t, createDatabase(context.Background(), filepath.Join(t.TempDir(), databaseName)))
	})
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

// TestConcurrentFirstOpensShareOneDatabase starts several processes that open
// the same missing database at once. Each must either open it or report
// retryable instability, and none may see a rollback journal. The openers are
// separate processes, as concurrent first starts are: SQLite's POSIX locks
// belong to a process, and the preflight closing its own handle to a database
// file would release the locks of another connection in the same process.
func TestConcurrentFirstOpensShareOneDatabase(t *testing.T) {
	assertConcurrentFirstOpensShareOneDatabase(t, false)
}

// TestConcurrentFirstOpensShareOneDatabaseUnderTheCreationLock repeats the
// concurrent first opens where only the locked rename can publish.
func TestConcurrentFirstOpensShareOneDatabaseUnderTheCreationLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows publishes with MoveFileEx only")
	}
	assertConcurrentFirstOpensShareOneDatabase(t, true)
}

const (
	firstOpenStateEnv  = "OWNGIT_FIRST_OPEN_HELPER_STATE"
	firstOpenSignalEnv = "OWNGIT_FIRST_OPEN_HELPER_SIGNAL"
	firstOpenLockedEnv = "OWNGIT_FIRST_OPEN_HELPER_LOCKED"
	firstOpenResult    = "first-open-result: "
)

// TestFirstOpenHelperProcess is one opener of the concurrent first-open
// tests. It announces itself with SIGNAL.ready-PID, waits for SIGNAL.go, opens
// the state directory and prints the outcome.
func TestFirstOpenHelperProcess(t *testing.T) {
	directory := os.Getenv(firstOpenStateEnv)
	if directory == "" {
		t.Skip("helper process only")
	}
	if os.Getenv(firstOpenLockedEnv) != "" {
		publishers.renameExclusive = failWith(syscall.ENOTSUP)
		publishers.link = failWith(syscall.EPERM)
	}
	signal := os.Getenv(firstOpenSignalEnv)
	noErr(t, os.WriteFile(fmt.Sprintf("%s.ready-%d", signal, os.Getpid()), nil, 0o600))
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(time.Millisecond) {
		if _, err := os.Stat(signal + ".go"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Println(firstOpenResult + "error: no start signal")
			return
		}
	}
	store, err := Open(context.Background(), directory)
	if err == nil {
		err = store.Close()
	}
	switch {
	case err == nil:
		fmt.Println(firstOpenResult + "ok")
	case errors.Is(err, ErrInspectionUnstable):
		fmt.Println(firstOpenResult + "unstable: " + err.Error())
	default:
		fmt.Println(firstOpenResult + "error: " + err.Error())
	}
}

func assertConcurrentFirstOpensShareOneDatabase(t *testing.T, locked bool) {
	t.Helper()
	ctx := context.Background()
	const openers = 6
	for attempt := 0; attempt < 5; attempt++ {
		root := t.TempDir()
		directory := filepath.Join(root, "state")
		signal := filepath.Join(root, "signal")
		children := make([]*exec.Cmd, openers)
		outputs := make([]*strings.Builder, openers)
		for index := range children {
			child := exec.Command(os.Args[0], "-test.run=^TestFirstOpenHelperProcess$", "-test.count=1", "-test.v")
			child.Env = append(os.Environ(), firstOpenStateEnv+"="+directory, firstOpenSignalEnv+"="+signal)
			if locked {
				child.Env = append(child.Env, firstOpenLockedEnv+"=1")
			}
			outputs[index] = &strings.Builder{}
			child.Stdout, child.Stderr = outputs[index], outputs[index]
			noErr(t, child.Start())
			children[index] = child
			// A failed attempt stops the test before Wait; the process
			// must not outlive it.
			t.Cleanup(func() {
				if child.ProcessState == nil {
					_ = child.Process.Kill()
					_ = child.Wait()
				}
			})
		}
		// Wait until every opener is ready, so they start together. The
		// bound only guards against a hang.
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(5 * time.Millisecond) {
			ready, err := filepath.Glob(signal + ".ready-*")
			noErr(t, err)
			if len(ready) == openers {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("attempt %d: %d of %d openers became ready", attempt, len(ready), openers)
			}
		}
		noErr(t, os.WriteFile(signal+".go", nil, 0o600))
		for index, child := range children {
			waitErr := child.Wait()
			output := outputs[index].String()
			line := ""
			if at := strings.Index(output, firstOpenResult); at >= 0 {
				line, _, _ = strings.Cut(output[at+len(firstOpenResult):], "\n")
			}
			if waitErr != nil || !(line == "ok" || strings.HasPrefix(line, "unstable: ")) {
				t.Fatalf("attempt %d opener %d: %q (exit: %v)\n%s", attempt, index, line, waitErr, output)
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
