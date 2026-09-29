package recovery

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// assertEmpty stops the test unless dir holds nothing.
func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("%s holds %v (%v)", dir, entries, err)
	}
}

// A temporary place without room for the repositories stops the rehearsal
// before anything is restored, naming the place, and no repository is
// reported as failed.
func TestVerifyRefusesAPlaceWithoutRoom(t *testing.T) {
	backup := newTwoRepositoryBackup(t, t.TempDir())
	temporary := t.TempDir()
	defer func(saved func(string) (uint64, bool, error)) { freeSpace = saved }(freeSpace)
	freeSpace = func(string) (uint64, bool, error) { return 100, true, nil }

	result, err := Verify(context.Background(), backup, temporary, "")
	var space *SpaceError
	if !errors.As(err, &space) || result.Verified || !strings.Contains(result.Error, "not enough free space in "+space.Dir) || space.Dir == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, item := range result.Repositories {
		if item.Status != VerifyNotRun {
			t.Errorf("repository %+v, want not run", item)
		}
	}
	assertEmpty(t, temporary)
}

// A bundle that is a sparse file of 1 TiB with the manifest's old digest
// ends quickly: the space check refuses it before it is read, or, on a disk
// with that much room, the deadline stops the copy.
func TestVerifyEndsOnAHugeSparseBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sparse files are made differently on Windows")
	}
	backup := newTwoRepositoryBackup(t, t.TempDir())
	temporary := t.TempDir()
	noErr(t, os.Truncate(filepath.Join(backup, "repositories", "project.bundle"), 1<<40))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	result, err := Verify(ctx, backup, temporary, "")
	if err == nil || result.Verified || time.Since(started) > 20*time.Second {
		t.Fatalf("after %v: result=%+v err=%v", time.Since(started), result, err)
	}
	assertEmpty(t, temporary)
}

// The copy that restore reads stops when its context ends, and nothing of
// it stays.
func TestBundleCopyStopsWhenCancelled(t *testing.T) {
	inputRoot, stage := t.TempDir(), t.TempDir()
	noErr(t, os.Mkdir(filepath.Join(inputRoot, "repositories"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(inputRoot, "repositories", "project.bundle"), make([]byte, 1<<20), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	item := RepositoryManifest{ID: "project", Bundle: "repositories/project.bundle"}
	if _, err := copyBundle(ctx, inputRoot, stage, item); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy error=%v, want cancellation", err)
	}
	assertEmpty(t, stage)

	// A link in place of the bundle is refused.
	if runtime.GOOS != "windows" {
		link := filepath.Join(inputRoot, "repositories", "link.bundle")
		noErr(t, os.Symlink(filepath.Join(inputRoot, "repositories", "project.bundle"), link))
		item = RepositoryManifest{ID: "link", Bundle: "repositories/link.bundle"}
		if _, err := copyBundle(context.Background(), inputRoot, stage, item); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("copy of a link: %v", err)
		}
		assertEmpty(t, stage)
	}
}

// A bundle that grows while it is checked cannot keep the check going:
// restore reads the size the bundle had when it was opened, so the rehearsal
// ends, and it restores only the bytes it checked.
func TestVerifyEndsWhileABundleGrows(t *testing.T) {
	backup := newTwoRepositoryBackup(t, t.TempDir())
	temporary := t.TempDir()
	file, err := os.OpenFile(filepath.Join(backup, "repositories", "project.bundle"), os.O_APPEND|os.O_WRONLY, 0)
	noErr(t, err)
	stop := make(chan struct{})
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		chunk := make([]byte, 64<<10)
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := file.Write(chunk); err != nil {
					return
				}
			}
		}
	}()
	result, err := Verify(context.Background(), backup, temporary, "")
	close(stop)
	group.Wait()
	noErr(t, file.Close())
	for _, item := range result.Repositories {
		if item.ID == "project" && item.Status == VerifyFailed && !strings.Contains(item.Error, "checksum mismatch") {
			t.Fatalf("project failed with %q, want a checksum mismatch", item.Error)
		}
	}
	if result.Verified != (err == nil) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertEmpty(t, temporary)
}

// Many repositories, one of them several MiB of data that does not
// compress, verify; every repository is named with its result.
func TestVerifyManyRepositoriesAndALargeOne(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	work := filepath.Join(root, "backup-work")
	large := make([]byte, 16<<20)
	_, err := rand.Read(large)
	noErr(t, err)
	noErr(t, os.WriteFile(filepath.Join(work, "large.bin"), large, 0o600))
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "large")
	project, err := manager.Path("project")
	noErr(t, err)
	runGit(t, work, "push", project, "HEAD:refs/heads/main")
	const count = 40
	for index := range count {
		if _, err := manager.Create(ctx, fmt.Sprintf("repository-%02d", index), ""); err != nil {
			t.Fatal(err)
		}
	}
	backup := filepath.Join(root, "backup")
	noErr(t, Create(ctx, store, manager, backup))

	temporary := t.TempDir()
	result, err := Verify(ctx, backup, temporary, "")
	noErr(t, err)
	if !result.Verified || len(result.Repositories) != count+1 {
		t.Fatalf("result=%+v", result)
	}
	for _, item := range result.Repositories {
		if item.Status != VerifyPassed {
			t.Errorf("repository %+v, want passed", item)
		}
	}
	assertEmpty(t, temporary)
}

// A full disk is recognized as Go, Git and SQLite report it.
func TestDiskFullIsRecognized(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("write: %w", syscall.ENOSPC),
		errors.New("fetch: exit status 128: fatal: write error: No space left on device"),
		errors.New("database or disk is full (13)"),
	} {
		if !diskFull(err) {
			t.Errorf("%v is not taken for a full disk", err)
		}
	}
	if diskFull(errors.New("bundle checksum mismatch")) || diskFull(nil) {
		t.Error("another error was taken for a full disk")
	}
}
