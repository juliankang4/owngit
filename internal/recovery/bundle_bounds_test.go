package recovery

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// A bundle larger than the room left where the rehearsal runs stops the
// verification before anything is read or restored, naming that place; no
// repository is reported as failed. The bundle is a sparse file of half the
// free space plus 1 GiB, which with the copy that restore makes of it needs
// more than the free space, although it takes almost none.
func TestVerifyRefusesAPlaceWithoutRoom(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file of that size would take its space on NTFS; sparse files need a separate call there")
	}
	backup := newTwoRepositoryBackup(t, t.TempDir())
	temporary := t.TempDir()
	free, known, err := diskFreeSpace(temporary)
	noErr(t, err)
	if !known {
		t.Skip("this system does not tell the free space")
	}
	size := free/2 + 1<<30
	if err := os.Truncate(filepath.Join(backup, "repositories", "project.bundle"), int64(size)); err != nil {
		t.Skipf("the file system refuses a sparse file of %d MiB: %v", size>>20, err)
	}

	result, err := Verify(context.Background(), backup, temporary, "")
	var space *SpaceError
	if !errors.As(err, &space) || space.Err != nil || result.Verified || !strings.Contains(result.Error, "not enough free space in "+space.Dir) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, item := range result.Repositories {
		if item.Status != VerifyNotRun {
			t.Errorf("repository %+v, want not run", item)
		}
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
		// 4 MiB at most, a little at a time, for as long as the
		// rehearsal runs.
		chunk := make([]byte, 64<<10)
		for range 64 {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
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

// The room a restore needs cannot wrap around to a small number, however
// large the bundles claim to be.
func TestRoomNeededSaturates(t *testing.T) {
	if got := roomNeeded([]uint64{1 << 62, 1 << 62, 1 << 62}); got != math.MaxUint64 {
		t.Fatalf("roomNeeded of three 4 EiB bundles=%d, want the largest value", got)
	}
	if got := roomNeeded([]uint64{math.MaxUint64}); got != math.MaxUint64 {
		t.Fatalf("roomNeeded of one huge bundle=%d", got)
	}
	if got := roomNeeded([]uint64{3, 5}); got != 13 {
		t.Fatalf("roomNeeded(3, 5)=%d, want 13", got)
	}
}

// A full disk is recognized as Go, Git and SQLite report it.
func TestDiskFullIsRecognized(t *testing.T) {
	for _, err := range append(systemDiskFullErrors(),
		errors.New("fetch: exit status 128: fatal: write error: No space left on device"),
		errors.New("database or disk is full (13)"),
	) {
		if !diskFull(err) {
			t.Errorf("%v is not taken for a full disk", err)
		}
	}
	if diskFull(errors.New("bundle checksum mismatch")) || diskFull(nil) {
		t.Error("another error was taken for a full disk")
	}
}
