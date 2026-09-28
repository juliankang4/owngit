//go:build windows

package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
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

// Run as administrator, OwnGit uses no folder on a share, not even for its
// log, and no account keeps its state there. The share is this computer's
// own administrative share, which only administrators reach.
func TestWindowsAdministratorUsesNoFolderOnAShare(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated administrator")
	}
	local := t.TempDir()
	volume := filepath.VolumeName(local)
	if len(volume) != 2 || volume[1] != ':' {
		t.Skipf("%s is not on a drive letter", local)
	}
	share := `\\localhost\` + volume[:1] + `$` + local[len(volume):]
	if _, err := os.Stat(share); err != nil {
		t.Skipf("the administrative share is unavailable: %v", err)
	}
	dir, err := OpenDirectory(filepath.Join(share, "logs"), true)
	if dir != nil {
		dir.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "network share") || !strings.Contains(err.Error(), "run as administrator") {
		t.Errorf("a log folder on a share: error=%v, want it refused", err)
	}
	held, err := CreateDirectory(filepath.Join(share, "state"))
	if held != nil {
		held.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "only on a local disk") {
		t.Errorf("state on a share: error=%v, want it refused", err)
	}
	for _, name := range []string{"logs", "state"} {
		if _, err := os.Lstat(filepath.Join(local, name)); !os.IsNotExist(err) {
			t.Errorf("%s was created on the share: %v", name, err)
		}
	}
}
