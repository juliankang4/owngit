//go:build windows

package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A junction on the way to a folder for OwnGit's files, at its name or
// above it, is refused: whoever made it chose where OwnGit would write.
func TestWindowsOpenDirectoryRefusesAJunctionOnTheWay(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	noErr(t, os.MkdirAll(filepath.Join(target, "state"), 0o700))
	junction := filepath.Join(root, "junction")
	if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
	for _, path := range []string{junction, filepath.Join(junction, "state"), filepath.Join(junction, "new")} {
		for _, create := range []bool{false, true} {
			dir, err := OpenDirectory(path, create)
			if dir != nil {
				dir.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "is a link or junction") {
				t.Errorf("OpenDirectory(%s, %t) error=%v, want the junction refused", path, create, err)
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(target, "new")); !os.IsNotExist(err) {
		t.Fatalf("a folder was created through the junction: %v", err)
	}
}

// linkTestFolder makes link a junction to target.
func linkTestFolder(t *testing.T, target, link string) {
	t.Helper()
	if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
}
