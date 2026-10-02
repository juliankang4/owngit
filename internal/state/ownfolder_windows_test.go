//go:build windows

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsOpenOwnFolderInDoesNotFollowLinksOrChangeDACL(t *testing.T) {
	root := t.TempDir()
	parent, err := os.Open(root)
	noErr(t, err)
	defer parent.Close()
	child := filepath.Join(root, "child")
	noErr(t, os.Mkdir(child, 0o700))
	before := securityDescriptorString(t, child)

	held, err := OpenOwnFolderIn(parent, "child")
	noErr(t, err)
	noErr(t, held.Close())
	if after := securityDescriptorString(t, child); after != before {
		t.Fatalf("DACL changed:\n%s\n%s", before, after)
	}

	if err := os.Symlink(child, filepath.Join(root, "link")); err == nil {
		if linked, err := OpenOwnFolderIn(parent, "link"); err == nil {
			linked.Close()
			t.Fatal("folder symlink was followed")
		}
	}
	noErr(t, os.WriteFile(filepath.Join(root, "file"), nil, 0o600))
	if file, err := OpenOwnFolderIn(parent, "file"); err == nil {
		file.Close()
		t.Fatal("regular file was accepted as a folder")
	}
}

func TestWindowsOpenOwnFolderInRefusesAnotherOwner(t *testing.T) {
	system := os.Getenv("SystemRoot")
	if system == "" {
		t.Skip("SystemRoot is not set")
	}
	parent, err := os.Open(filepath.Dir(system))
	noErr(t, err)
	defer parent.Close()
	if folder, err := OpenOwnFolderIn(parent, filepath.Base(system)); err == nil {
		folder.Close()
		t.Fatal("a system-owned folder was accepted")
	}
}
