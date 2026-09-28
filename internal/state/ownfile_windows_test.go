//go:build windows

package state

import (
	"os"
	"path/filepath"
	"testing"
)

// A lock file is protected only once it is known to be a file of this
// account with one name: a second name, or a link where the account may
// make one, put where the lock goes leaves the other file's access list as
// it was.
func TestWindowsLockChangesNoFileAtItsName(t *testing.T) {
	for _, plant := range []struct {
		name  string
		place func(target, lock string) error
	}{
		{"second name", os.Link},
		{"symbolic link", os.Symlink},
	} {
		t.Run(plant.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "program.txt")
			noErr(t, os.WriteFile(target, []byte("kept\n"), 0o644))
			before, err := protectionFingerprint(target)
			noErr(t, err)
			lock := filepath.Join(t.TempDir(), "operation.lock")
			if err := plant.place(target, lock); err != nil {
				t.Skipf("cannot make a %s here: %v", plant.name, err)
			}
			if release, err := AcquireExclusiveFileLock(lock); err == nil {
				release()
				t.Error("the planted lock name was locked")
			}
			after, err := protectionFingerprint(target)
			noErr(t, err)
			if after != before {
				t.Fatalf("the planted file's access list changed:\n%s\n%s", before, after)
			}
		})
	}
}
