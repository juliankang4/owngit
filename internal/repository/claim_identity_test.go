package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The claim must follow the folder and lock file it was taken on, not their
// names: a removed lock name lets a second server claim the same folder, and a
// replaced folder is not the claimed one.
func TestStorageClaimStopsWritesWhenFolderOrLockNameChanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to remove or rename the held lock file and folder")
	}
	for _, change := range []string{"lock unlinked", "folder replaced"} {
		t.Run(change, func(t *testing.T) {
			first, _, _ := newTestRepository(t)
			noErr(t, first.ClaimStorage())
			t.Cleanup(first.ReleaseStorage)
			root := first.RepositoryRoot()
			if change == "lock unlinked" {
				noErr(t, os.Remove(filepath.Join(root, storageLockName)))
				second := secondServer(first)
				noErr(t, second.ClaimStorage(), "second server claims after unlink")
				t.Cleanup(second.ReleaseStorage)
			} else {
				noErr(t, os.Rename(root, root+"-moved"))
				noErr(t, os.Mkdir(root, 0o700))
			}
			if _, err := first.Create(context.Background(), "after-change", ""); !errors.Is(err, ErrStorageChanged) {
				t.Fatalf("create after %s error=%v, want ErrStorageChanged", change, err)
			}
			if _, err := os.Lstat(filepath.Join(root, "after-change.git")); !os.IsNotExist(err) {
				t.Fatalf("write went through: %v", err)
			}
			// Every other repository write stops at the same check.
			if _, err := first.Delete(context.Background(), "sample", DeleteFiles); !errors.Is(err, ErrStorageChanged) {
				t.Fatalf("delete after %s error=%v, want ErrStorageChanged", change, err)
			}
			if err := first.Locks.For("sample").LockContext(context.Background()); !errors.Is(err, ErrStorageChanged) {
				t.Fatalf("write lock after %s error=%v, want ErrStorageChanged", change, err)
			}
			if _, err := first.Rename(context.Background(), "sample", "renamed", time.Now()); !errors.Is(err, ErrStorageChanged) {
				t.Fatalf("rename after %s error=%v, want ErrStorageChanged", change, err)
			}
		})
	}
}

// A directory substituted for the staging directory is neither published nor
// removed by the path-based cleanup.
func TestCreateIgnoresASubstitutedStagingDirectory(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	var substitute string
	manager.creationDirectoryHook = func(path string) {
		noErr(t, os.Rename(path, path+"-original"))
		noErr(t, os.Mkdir(path, 0o700))
		noErr(t, os.WriteFile(filepath.Join(path, "keep"), []byte("other data"), 0o600))
		substitute = path
	}
	if _, err := manager.Create(context.Background(), "swapped", ""); err == nil {
		t.Fatal("creation succeeded with a substituted staging directory")
	}
	if _, err := os.Lstat(filepath.Join(manager.RepositoryRoot(), "swapped.git")); !os.IsNotExist(err) {
		t.Fatalf("substitute was published: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(substitute, "keep")); err != nil || string(data) != "other data" {
		t.Fatalf("substitute was removed or changed: %q %v", data, err)
	}
}

// A replacement that takes the name between the identity check and the rename
// is not kept as this attempt's creation.
func TestCreationRollbackReturnsAReplacementThatTookTheName(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	path := filepath.Join(manager.RepositoryRoot(), "draft")
	noErr(t, os.Mkdir(path, 0o700))
	noErr(t, manager.InitBareRepository(context.Background(), path, CreateOptions{}))
	creation, err := captureEmptyCreation(path)
	noErr(t, err)
	t.Cleanup(func() { _ = creation.parent.Close() })
	creation.beforeMove = func() {
		noErr(t, os.Rename(path, path+"-original"))
		noErr(t, os.Mkdir(path, 0o700))
		noErr(t, os.WriteFile(filepath.Join(path, "keep"), []byte("other data"), 0o600))
	}
	if err := creation.rollback(path); err == nil {
		t.Fatal("rollback accepted a replacement")
	}
	if data, err := os.ReadFile(filepath.Join(path, "keep")); err != nil || string(data) != "other data" {
		t.Fatalf("replacement was moved away: %q %v", data, err)
	}
}

// Removing an obsolete hook goes through the held folder: a folder renamed
// away and replaced under the same path loses nothing.
func TestRemoveInHeldDirectoryRefusesAReplacedFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to rename a folder that is held open")
	}
	hooks := filepath.Join(t.TempDir(), "hooks")
	noErr(t, os.Mkdir(hooks, 0o700))
	held, err := os.Open(hooks)
	noErr(t, err)
	t.Cleanup(func() { _ = held.Close() })
	noErr(t, os.Rename(hooks, hooks+"-moved"))
	noErr(t, os.Mkdir(hooks, 0o700))
	noErr(t, os.WriteFile(filepath.Join(hooks, "pre-receive"), []byte("other"), 0o600))
	if err := removeInHeldDirectory(held, "pre-receive"); err == nil {
		t.Fatal("removal went through a replaced folder")
	}
	if _, err := os.Stat(filepath.Join(hooks, "pre-receive")); err != nil {
		t.Fatalf("file in the replacement was removed: %v", err)
	}
}

// A write lock taken while the folder is still an empty mount point must not
// claim it: after the share mounts over the path, writes still work.
func TestWriteLockDoesNotClaimAnEmptyMountPoint(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	share := manager.RepositoryRoot()
	mountPoint := filepath.Join(t.TempDir(), "mount-point")
	noErr(t, os.Mkdir(mountPoint, 0o700))
	manager.SetRoot(mountPoint)
	noErr(t, manager.ClaimStorage())
	t.Cleanup(manager.ReleaseStorage)
	lock := manager.Locks.For("waiting")
	noErr(t, lock.LockContext(context.Background()))
	lock.Unlock()
	if _, err := os.Lstat(filepath.Join(mountPoint, storageLockName)); !os.IsNotExist(err) {
		t.Fatalf("a write lock claimed the empty mount point: %v", err)
	}
	// The share mounts over the path.
	noErr(t, os.Rename(mountPoint, mountPoint+"-local"))
	noErr(t, os.Rename(share, mountPoint))
	if _, err := manager.Create(context.Background(), "after-mount", ""); err != nil {
		t.Fatalf("create after the share mounted: %v", err)
	}
}
