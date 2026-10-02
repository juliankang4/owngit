//go:build windows

package state

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsOthersCanChangeFileInspectsHeldFolderAfterReplacement(t *testing.T) {
	root := t.TempDir()
	parent, err := os.Open(root)
	noErr(t, err)
	defer parent.Close()
	path := filepath.Join(root, "child")
	noErr(t, MkdirPrivate(path))
	held, err := OpenOwnFolderIn(parent, "child")
	noErr(t, err)
	defer held.Close()
	info, err := held.Stat()
	noErr(t, err)
	noErr(t, os.Rename(path, filepath.Join(root, "held")))
	noErr(t, os.Mkdir(path, 0o700))
	user, _, err := processIdentity()
	noErr(t, err)
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	noErr(t, err)
	setRawDACL(t, path, true, []windows.EXPLICIT_ACCESS{
		testEntry(user, windows.GRANT_ACCESS, fileAllAccess),
		testEntry(everyone, windows.GRANT_ACCESS, windows.FILE_WRITE_DATA),
	}, false)

	changeable, err := OthersCanChangeFile(held, info)
	if err != nil || changeable {
		t.Fatalf("held private folder changeable=%v err=%v", changeable, err)
	}
	pathInfo, err := os.Stat(path)
	noErr(t, err)
	if pathChangeable, _, err := OthersCanChange(path, pathInfo); err != nil || !pathChangeable {
		t.Fatalf("replacement control changeable=%v err=%v", pathChangeable, err)
	}
}

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
