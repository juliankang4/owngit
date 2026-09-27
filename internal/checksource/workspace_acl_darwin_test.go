//go:build darwin

package checksource

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func addMacOSACL(t *testing.T, path, entry string) {
	t.Helper()
	if output, err := exec.Command("chmod", "+a", entry, path).CombinedOutput(); err != nil {
		t.Fatalf("chmod +a: %v: %s", err, output)
	}
	// A deny delete entry would stop the test's own cleanup.
	t.Cleanup(func() { _ = exec.Command("chmod", "-N", path).Run() })
}

func TestAcquireWorkspaceRootRefusesMacOSACLThatAllowsChanges(t *testing.T) {
	scratch := resolvedTempDir(t)
	parent := directoryWithMode(t, filepath.Join(scratch, "acl"), 0o755)
	addMacOSACL(t, parent, "everyone allow add_subdirectory,delete_child")
	root := filepath.Join(parent, "workspace")
	_, err := AcquireWorkspaceRoot(root)
	requireUnsafeParent(t, err, root, parent)
	var unsafe *UnsafeWorkspaceRootError
	if errors.As(err, &unsafe) && unsafe.Fix != "chmod -N '"+parent+"'" {
		t.Fatalf("refusal fix %q, want chmod -N", unsafe.Fix)
	}
	requireNoWorkspaceBelow(t, parent)

	// The workspace root's own list is checked before anything changes.
	existing := directoryWithMode(t, filepath.Join(scratch, "existing"), 0o755)
	addMacOSACL(t, existing, "everyone allow add_file")
	_, err = AcquireWorkspaceRoot(existing)
	requireUnsafeParent(t, err, existing, existing)
	assertUntouchedWorkspaceRoot(t, existing, os.ModeDir|0o755)

	// Control: an entry that only denies, like the one on home folders.
	denied := directoryWithMode(t, filepath.Join(scratch, "denied"), 0o755)
	addMacOSACL(t, denied, "everyone deny delete")
	workspace, err := AcquireWorkspaceRoot(filepath.Join(denied, "workspace"))
	noErr(t, err)
	workspace.Close()
}
