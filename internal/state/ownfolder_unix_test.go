//go:build !windows

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenOwnFolderInDoesNotFollowLinksOrChangePermissions(t *testing.T) {
	root := t.TempDir()
	parent, err := os.Open(root)
	noErr(t, err)
	defer parent.Close()
	child := filepath.Join(root, "child")
	noErr(t, os.Mkdir(child, 0o700))
	noErr(t, os.Chmod(child, 0o755))
	before, err := os.Stat(child)
	noErr(t, err)

	held, err := OpenOwnFolderIn(parent, "child")
	noErr(t, err)
	noErr(t, held.Close())
	after, err := os.Stat(child)
	noErr(t, err)
	if before.Mode() != after.Mode() {
		t.Fatalf("mode changed from %v to %v", before.Mode(), after.Mode())
	}

	noErr(t, os.Symlink(child, filepath.Join(root, "link")))
	if linked, err := OpenOwnFolderIn(parent, "link"); err == nil {
		linked.Close()
		t.Fatal("folder symlink was followed")
	}
	noErr(t, os.WriteFile(filepath.Join(root, "file"), nil, 0o600))
	if file, err := OpenOwnFolderIn(parent, "file"); err == nil {
		file.Close()
		t.Fatal("regular file was accepted as a folder")
	}
}
