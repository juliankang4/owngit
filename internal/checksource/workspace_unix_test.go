//go:build !windows

package checksource

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// directoryWithMode makes a directory and sets its exact mode, which umask
// would otherwise narrow.
func directoryWithMode(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	noErr(t, os.Mkdir(path, 0o700))
	noErr(t, os.Chmod(path, mode))
	return path
}

// resolvedTempDir returns a test directory without links in its path, so it
// matches the directory names that a refusal reports.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	noErr(t, err)
	return directory
}

func requireUnsafeParent(t *testing.T, err error, root, directory string) {
	t.Helper()
	var unsafe *UnsafeWorkspaceRootError
	if !errors.As(err, &unsafe) || unsafe.Root != root || unsafe.Directory != directory {
		t.Fatalf("acquire err=%v, want a refusal naming %s", err, directory)
	}
}

func TestAcquireWorkspaceRootAdoptsEmptyRootOwnedByCurrentAccount(t *testing.T) {
	root := directoryWithMode(t, filepath.Join(t.TempDir(), "workspace"), 0o755)
	workspace, err := AcquireWorkspaceRoot(root)
	noErr(t, err)
	defer workspace.Close()
	info, err := os.Stat(root)
	noErr(t, err)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("adopted root mode %v, want 0700", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, workspaceRootMarker)); err != nil {
		t.Fatalf("adopted root has no ownership marker: %v", err)
	}
	if !OwnsWorkspaceDirectory(root) {
		t.Fatal("OwnsWorkspaceDirectory refused this account's root")
	}
}

func TestAcquireWorkspaceRootRefusesReplaceableParent(t *testing.T) {
	for _, mode := range []os.FileMode{0o777, 0o775} {
		shared := directoryWithMode(t, filepath.Join(resolvedTempDir(t), "shared"), mode)
		missing := filepath.Join(shared, "new")
		_, err := AcquireWorkspaceRoot(missing)
		requireUnsafeParent(t, err, missing, shared)
		if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused root was created: %v", err)
		}

		existing := directoryWithMode(t, filepath.Join(shared, "existing"), 0o755)
		_, err = AcquireWorkspaceRoot(existing)
		requireUnsafeParent(t, err, existing, shared)
		assertUntouchedWorkspaceRoot(t, existing, os.ModeDir|0o755)

		// A folder above the parent counts as well.
		nested := filepath.Join(existing, "nested", "root")
		_, err = AcquireWorkspaceRoot(nested)
		requireUnsafeParent(t, err, nested, shared)
	}
}

func TestAcquireWorkspaceRootAcceptsStickySharedParent(t *testing.T) {
	shared := directoryWithMode(t, filepath.Join(t.TempDir(), "shared"), os.ModeSticky|0o777)
	workspace, err := AcquireWorkspaceRoot(filepath.Join(shared, "workspace"))
	noErr(t, err)
	workspace.Close()
}

func TestAcquireWorkspaceRootChecksDirectoriesBehindLinks(t *testing.T) {
	scratch := resolvedTempDir(t)
	private := directoryWithMode(t, filepath.Join(scratch, "private"), 0o700)
	shared := directoryWithMode(t, filepath.Join(scratch, "shared"), 0o777)

	// A link in a folder that anyone can write can be pointed elsewhere.
	link := filepath.Join(shared, "link")
	noErr(t, os.Symlink(private, link))
	root := filepath.Join(link, "workspace")
	_, err := AcquireWorkspaceRoot(root)
	requireUnsafeParent(t, err, root, shared)

	// A link to a folder that anyone can write leads there.
	safeLink := filepath.Join(private, "to-shared")
	noErr(t, os.Symlink("../shared", safeLink))
	root = filepath.Join(safeLink, "workspace")
	_, err = AcquireWorkspaceRoot(root)
	requireUnsafeParent(t, err, root, shared)

	// A protected link to a protected folder is fine.
	noErr(t, os.Mkdir(filepath.Join(private, "target"), 0o700))
	noErr(t, os.Symlink("target", filepath.Join(private, "to-target")))
	workspace, err := AcquireWorkspaceRoot(filepath.Join(private, "to-target", "workspace"))
	noErr(t, err)
	workspace.Close()
}

// TestAcquireWorkspaceRootAsRootRefusesAnotherAccountsRoot needs root to give
// the directory another owner, so it runs only in a privileged environment.
func TestAcquireWorkspaceRootAsRootRefusesAnotherAccountsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create a directory owned by another account")
	}
	const otherUID, otherGID = 65534, 65534
	parent := t.TempDir()
	root := directoryWithMode(t, filepath.Join(parent, "workspace"), 0o700)
	noErr(t, os.Chown(root, otherUID, otherGID))

	_, err := AcquireWorkspaceRoot(root)
	var unsafe *UnsafeWorkspaceRootError
	if !errors.As(err, &unsafe) || unsafe.Directory != "" {
		t.Fatalf("root acquired another account's workspace: err=%v", err)
	}
	assertUntouchedWorkspaceRoot(t, root, os.ModeDir|0o700)
	info, err := os.Stat(root)
	noErr(t, err)
	if stat := info.Sys().(*syscall.Stat_t); stat.Uid != otherUID || stat.Gid != otherGID {
		t.Fatalf("refused root owner changed to %d:%d", stat.Uid, stat.Gid)
	}

	// Control: root's own empty directory is still adopted.
	own := directoryWithMode(t, filepath.Join(parent, "own"), 0o755)
	workspace, err := AcquireWorkspaceRoot(own)
	noErr(t, err)
	workspace.Close()
}
