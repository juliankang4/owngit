//go:build darwin

package state

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// Owners and modes are this computer's only on a filesystem macOS marks
// local; a network share or an unmarked FUSE filesystem is not.
func TestOwnershipIsEnforcedOnlyOnLocalFilesystems(t *testing.T) {
	if !ownershipEnforcedWith(unix.MNT_LOCAL | unix.MNT_JOURNALED) {
		t.Error("a local filesystem does not count as enforced")
	}
	if ownershipEnforcedWith(unix.MNT_NOSUID | unix.MNT_NODEV) {
		t.Error("a filesystem that is not marked local counts as enforced")
	}
	root, err := os.Open("/")
	noErr(t, err)
	defer root.Close()
	if enforced, err := ownershipEnforced(root); err != nil || !enforced {
		t.Errorf("the system volume: enforced=%t err=%v", enforced, err)
	}
}
