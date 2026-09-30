package server

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"owngit/internal/webui"
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
	root := t.TempDir()
	noErr(t, os.Mkdir(filepath.Join(root, "visible"), 0o700))
	hidden := filepath.Join(root, "hidden-attribute")
	noErr(t, os.Mkdir(hidden, 0o700))
	pointer, err := windows.UTF16PtrFromString(hidden)
	noErr(t, err)
	noErr(t, windows.SetFileAttributes(pointer, windows.FILE_ATTRIBUTE_HIDDEN))
	t.Cleanup(func() { _ = windows.SetFileAttributes(pointer, windows.FILE_ATTRIBUTE_NORMAL) })
	visible, err := listFolders(context.Background(), root, false, false)
	noErr(t, err)
	if len(visible.Folders) != 1 || visible.Folders[0].Name != "visible" {
		t.Fatalf("hidden attribute=%+v", visible)
	}
	all, err := listFolders(context.Background(), root, true, false)
	noErr(t, err)
	if len(all.Folders) != 2 {
		t.Fatalf("show hidden=%+v", all)
	}
}

func makeFolderJunction(t *testing.T, link, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("create junction: %v (%s)", err, output)
	}
}

func TestFolderChooserWindowsJunctions(t *testing.T) {
	app, store, _ := newTestApp(t)
	base := t.TempDir()
	root, target, gone := filepath.Join(base, "browse"), filepath.Join(base, "target"), filepath.Join(base, "gone")
	for _, path := range []string{root, target, gone, filepath.Join(root, "real"), filepath.Join(target, "inside")} {
		noErr(t, os.Mkdir(path, 0o700))
	}
	targetPointer, err := windows.UTF16PtrFromString(target)
	noErr(t, err)
	noErr(t, windows.SetFileAttributes(targetPointer, windows.FILE_ATTRIBUTE_HIDDEN))
	t.Cleanup(func() { _ = windows.SetFileAttributes(targetPointer, windows.FILE_ATTRIBUTE_NORMAL) })
	makeFolderJunction(t, filepath.Join(root, "junction"), target)
	makeFolderJunction(t, filepath.Join(root, "broken"), gone)
	noErr(t, os.Rename(gone, filepath.Join(base, "gone-moved")))
	noErr(t, store.StartApprovedSetupSession(context.Background(), "owner-session", "owner-csrf", time.Now().Add(time.Hour)))
	response := folderRequest(t, app, setupFoldersPath, root, "", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != 200 {
		t.Fatalf("junction listing: %d %s", response.Code, response.Body.String())
	}
	result := readFolderResult(t, response)
	if len(result.Folders) != 3 || result.Folders[0].Name != "broken" || result.Folders[1].Name != "junction" || result.Folders[1].Path != filepath.Join(root, "junction") || result.Folders[2].Name != "real" {
		t.Fatalf("junction listing=%+v", result)
	}
	response = folderRequest(t, app, setupFoldersPath, filepath.Join(root, "broken"), "", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != http.StatusNotFound || readFolderResult(t, response).Error != webui.MsgFolderMissing {
		t.Fatalf("open broken junction: %d %s", response.Code, response.Body.String())
	}
	response = folderRequest(t, app, setupFoldersPath, filepath.Join(root, "junction"), "", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != 200 || len(readFolderResult(t, response).Folders) != 1 {
		t.Fatalf("open junction: %d %s", response.Code, response.Body.String())
	}
	response = folderRequest(t, app, setupFolderCreatePath, filepath.Join(root, "junction"), "made", "owner-session", "owner-csrf", "localhost", "http://localhost")
	if response.Code != 200 {
		t.Fatalf("create through selected junction: %d %s", response.Code, response.Body.String())
	}
	info, err := os.Stat(filepath.Join(target, "made"))
	noErr(t, err)
	if !info.IsDir() {
		t.Fatal("selected junction target did not receive folder")
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
