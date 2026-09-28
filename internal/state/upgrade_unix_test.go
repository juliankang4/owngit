//go:build !windows

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The backup folder is private: a new one is made so, and an existing one
// that other accounts can change is refused and left as it is.
func TestUpgradeBackupFolderMustBePrivate(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	noErr(t, os.Mkdir(stateDir, 0o700))
	folder := UpgradeBackupFolder(stateDir)

	held, release, err := OpenUpgradeBackupFolder(stateDir)
	noErr(t, err)
	info, err := held.Stat()
	noErr(t, err)
	release()
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("new backup folder mode %v", info.Mode())
	}

	for _, mode := range []os.FileMode{0o777 | os.ModeSticky, 0o777} {
		noErr(t, os.Chmod(folder, mode))
		before, err := os.Stat(folder)
		noErr(t, err)
		_, _, err = OpenUpgradeBackupFolder(stateDir)
		if err == nil || !strings.Contains(err.Error(), "only this account can change") {
			t.Fatalf("mode %v: err=%v", mode, err)
		}
		after, err := os.Stat(folder)
		noErr(t, err)
		if after.Mode() != before.Mode() {
			t.Fatalf("mode %v was changed to %v", before.Mode(), after.Mode())
		}
	}
	// A folder others can only read stays usable.
	noErr(t, os.Chmod(folder, 0o755))
	_, release, err = OpenUpgradeBackupFolder(stateDir)
	noErr(t, err)
	release()
}

// Only a private folder of this account, not a link to one, is a backup
// that retention may look into.
func TestOpenPrivateFolderIn(t *testing.T) {
	root := t.TempDir()
	parent, err := os.Open(root)
	noErr(t, err)
	defer parent.Close()
	noErr(t, os.Mkdir(filepath.Join(root, "own"), 0o700))
	noErr(t, os.Mkdir(filepath.Join(root, "shared"), 0o777|os.ModeSticky))
	noErr(t, os.Chmod(filepath.Join(root, "shared"), 0o777|os.ModeSticky))
	noErr(t, os.Symlink(filepath.Join(root, "own"), filepath.Join(root, "link")))
	dir, err := OpenPrivateFolderIn(parent, "own")
	noErr(t, err)
	dir.Close()
	for _, name := range []string{"shared", "link"} {
		if dir, err := OpenPrivateFolderIn(parent, name); err == nil {
			dir.Close()
			t.Fatalf("%s was accepted", name)
		}
	}
}

// Turning the backup on removes only this account's own file at the
// setting's name, never what a link at that name leads to.
func TestTurningTheBackupOnRemovesNoLinkedFile(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	held, err := CreateDirectory(directory)
	noErr(t, err)
	defer held.Close()
	target := filepath.Join(t.TempDir(), "kept")
	noErr(t, os.WriteFile(target, []byte("kept"), 0o600))
	noErr(t, os.Symlink(target, filepath.Join(directory, upgradeBackupOffName)))
	if err := SetUpgradeBackup(held, true); err == nil {
		t.Fatal("a link at the setting's name was removed as the setting")
	}
	if _, err := os.Lstat(filepath.Join(directory, upgradeBackupOffName)); err != nil {
		t.Fatalf("the link was removed: %v", err)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "kept" {
		t.Fatalf("target=%q err=%v", content, err)
	}
}
