//go:build !windows

package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrivateDirectoryFixIsReadOnlyAndComplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared")
	noErr(t, os.Mkdir(path, 0o700))
	noErr(t, os.Chmod(path, 0o777))
	if runtime.GOOS == "darwin" {
		noErr(t, exec.Command("chmod", "+a", "everyone allow add_file,add_subdirectory,delete_child", path).Run())
	}
	before, err := os.Stat(path)
	noErr(t, err)
	fix, err := PrivateDirectoryFix(path, false)
	noErr(t, err)
	if !strings.Contains(fix, "chmod go-w") {
		t.Fatalf("root fix=%q", fix)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(fix, "chmod -N") {
		t.Fatalf("macOS root fix does not clear ACLs: %q", fix)
	}
	after, err := os.Stat(path)
	noErr(t, err)
	if before.Mode() != after.Mode() {
		t.Fatalf("building the fix changed mode from %v to %v", before.Mode(), after.Mode())
	}

	recursive, err := PrivateDirectoryFix(path, true)
	noErr(t, err)
	if !strings.Contains(recursive, "chmod -R go-rwx") {
		t.Fatalf("recursive fix=%q", recursive)
	}
	if runtime.GOOS == "darwin" && !strings.Contains(recursive, "chmod -RN") {
		t.Fatalf("recursive macOS fix does not clear ACLs: %q", recursive)
	}
}
