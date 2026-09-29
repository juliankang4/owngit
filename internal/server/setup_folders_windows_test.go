package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestFolderChooserWindowsDrivesAndHiddenAttributes(t *testing.T) {
	result, err := folderRoots(context.Background())
	noErr(t, err)
	mask, err := windows.GetLogicalDrives()
	noErr(t, err)
	var got uint32
	for _, drive := range result.Folders {
		if len(drive.Path) != 3 || drive.Path[1:] != `:\` || drive.Name != drive.Path {
			t.Fatalf("drive=%+v", drive)
		}
		got |= 1 << (drive.Path[0] - 'A')
		parent, roots := folderParent(drive.Path)
		if parent != "" || !roots {
			t.Fatalf("drive parent=%q roots=%v", parent, roots)
		}
	}
	if got != mask || mask == 0 || result.Path != "" {
		t.Fatalf("drives=%+v mask=%x", result, mask)
	}
	root := folderFixture(t)
	noErr(t, os.Mkdir(filepath.Join(root, "visible"), 0o700))
	hidden := filepath.Join(root, "hidden-attribute")
	noErr(t, os.Mkdir(hidden, 0o700))
	pointer, err := windows.UTF16PtrFromString(hidden)
	noErr(t, err)
	noErr(t, windows.SetFileAttributes(pointer, windows.FILE_ATTRIBUTE_HIDDEN))
	t.Cleanup(func() { _ = windows.SetFileAttributes(pointer, windows.FILE_ATTRIBUTE_NORMAL) })
	visible, err := listFolders(context.Background(), root, false)
	noErr(t, err)
	if len(visible.Folders) != 1 || visible.Folders[0].Name != "visible" {
		t.Fatalf("hidden attribute=%+v", visible)
	}
	all, err := listFolders(context.Background(), root, true)
	noErr(t, err)
	if len(all.Folders) != 2 {
		t.Fatalf("show hidden=%+v", all)
	}
}

func TestFolderChooserWindowsNames(t *testing.T) {
	for _, name := range []string{"CON", "con.txt", "PRN", "AUX", "NUL", "COM1", "COM9.log", "LPT1", "LPT9", "COM¹", "LPT².txt", "CONIN$", "CONOUT$", "CON .txt", "folder.", "folder ", "a:b", "a?b", "a*b", "a<b", `a"b`, "control\x01"} {
		if validFolderName(name) {
			t.Errorf("reserved name accepted: %q", name)
		}
	}
	for _, name := range []string{"repositories", "한글", "COM10", "COM0", "CONSOLE", "a.b", strings.Repeat("a", 255), strings.Repeat("한", 100)} {
		if !validFolderName(name) {
			t.Errorf("valid name refused: %q", name)
		}
	}
}
