//go:build windows

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// While OpenUpgradeBackupFolder holds the backup folder, neither it nor a
// folder on the way to it can be renamed, so no other folder can take its
// place; after release both can.
func TestWindowsBackupFolderCannotBeRenamedWhileHeld(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	stateDir := filepath.Join(parent, "state")
	noErr(t, os.MkdirAll(stateDir, 0o700))
	folder := UpgradeBackupFolder(stateDir)
	held, release, err := OpenUpgradeBackupFolder(stateDir)
	noErr(t, err)
	noErr(t, requirePrivateFolder(held))
	if err := os.Rename(folder, folder+"-moved"); err == nil {
		release()
		t.Fatal("the held backup folder was renamed")
	}
	if err := os.Rename(parent, parent+"-moved"); err == nil {
		release()
		t.Fatal("a folder on the way to the held backup folder was renamed")
	}
	// Work inside the held folder still works.
	noErr(t, os.Mkdir(filepath.Join(folder, "stage"), 0o700))
	noErr(t, os.Rename(filepath.Join(folder, "stage"), filepath.Join(folder, "published")))
	release()
	noErr(t, os.Rename(folder, folder+"-moved"))
}

// An existing backup folder with an inherited access list is refused and
// left as it is; a new one is made private.
func TestWindowsExistingBackupFolderMustBePrivate(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	noErr(t, os.MkdirAll(stateDir, 0o700))
	folder := UpgradeBackupFolder(stateDir)
	noErr(t, os.Mkdir(folder, 0o700))
	before, err := pathDescriptor(folder)
	noErr(t, err)
	_, _, err = OpenUpgradeBackupFolder(stateDir)
	if err == nil || !strings.Contains(err.Error(), "only this account can change") {
		t.Fatalf("err=%v", err)
	}
	after, err := pathDescriptor(folder)
	noErr(t, err)
	if before.String() != after.String() {
		t.Fatalf("the refused folder's access list changed:\n%s\n%s", before, after)
	}
	noErr(t, os.Remove(folder))
	_, release, err := OpenUpgradeBackupFolder(stateDir)
	noErr(t, err)
	release()
}
