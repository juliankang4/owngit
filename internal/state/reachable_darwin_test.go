//go:build darwin

package state

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestExposureCheckDoesNotTreatAncestorACLFailureAsPrivate(t *testing.T) {
	parent := t.TempDir()
	targetPath := filepath.Join(parent, "repositories")
	noErr(t, os.Mkdir(targetPath, 0o777))
	noErr(t, os.Chmod(parent, 0o700))
	noErr(t, os.Chmod(targetPath, 0o777))
	target, err := os.Open(targetPath)
	noErr(t, err)
	defer target.Close()
	info, err := target.Stat()
	noErr(t, err)

	original := getattrlistErr
	t.Cleanup(func() { getattrlistErr = original })
	getattrlistErr = func(trap, a1, a2, a3, a4, a5, a6 uintptr) unix.Errno {
		if trap == unix.SYS_FGETATTRLIST {
			return unix.EIO
		}
		return original(trap, a1, a2, a3, a4, a5, a6)
	}
	if exposed, err := ExposedToOtherAccounts(targetPath, target, info); err == nil || exposed {
		t.Fatalf("ACL inspection failure exposed=%v err=%v", exposed, err)
	}
}
