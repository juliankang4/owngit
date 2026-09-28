package state

import (
	"os"
	"path/filepath"
	"testing"
)

// Files are opened and renamed in the folder that was opened, held: after
// the folder's name is given to a link to another folder, nothing reaches
// the other folder. On Windows the link is a junction.
func TestOwnFilesStayInTheHeldFolder(t *testing.T) {
	root := t.TempDir()
	folder, other, moved := filepath.Join(root, "folder"), filepath.Join(root, "other"), filepath.Join(root, "moved")
	noErr(t, os.Mkdir(folder, 0o700))
	noErr(t, os.Mkdir(other, 0o700))
	noErr(t, os.WriteFile(filepath.Join(other, "kept"), []byte("kept\n"), 0o600))
	dir, err := OpenDirectory(folder, false)
	noErr(t, err)
	defer dir.Close()
	noErr(t, os.Rename(folder, moved))
	linkTestFolder(t, other, folder)
	file, err := OpenOwnFile(dir, "kept", os.O_RDWR|os.O_CREATE)
	noErr(t, err)
	noErr(t, file.Close())
	noErr(t, RenameOwnFile(dir, "kept", "renamed"))
	if content, err := os.ReadFile(filepath.Join(other, "kept")); err != nil || string(content) != "kept\n" {
		t.Fatalf("the other folder's file now holds %q (%v)", content, err)
	}
	if _, err := os.Lstat(filepath.Join(moved, "renamed")); err != nil {
		t.Fatalf("the held folder lacks the renamed file: %v", err)
	}
}
