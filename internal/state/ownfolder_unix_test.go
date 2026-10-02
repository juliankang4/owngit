//go:build !windows

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOthersCanChangeFileInspectsHeldFolderAfterPathReplacement(t *testing.T) {
	root := t.TempDir()
	parent, err := os.Open(root)
	noErr(t, err)
	defer parent.Close()
	path := filepath.Join(root, "child")
	noErr(t, os.Mkdir(path, 0o700))
	held, err := OpenOwnFolderIn(parent, "child")
	noErr(t, err)
	defer held.Close()
	info, err := held.Stat()
	noErr(t, err)

	moved := filepath.Join(root, "held")
	noErr(t, os.Rename(path, moved))
	target := filepath.Join(t.TempDir(), "outside")
	noErr(t, os.Mkdir(target, 0o700))
	noErr(t, os.Chmod(target, 0o777))
	noErr(t, os.Symlink(target, path))
	changeable, err := OthersCanChangeFile(held, info)
	if err != nil || changeable {
		t.Fatalf("held private folder changeable=%v err=%v", changeable, err)
	}
	targetInfo, err := os.Stat(target)
	noErr(t, err)
	if targetChangeable, _, err := OthersCanChange(target, targetInfo); err != nil || !targetChangeable {
		t.Fatalf("outside control changeable=%v err=%v", targetChangeable, err)
	}
}

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
