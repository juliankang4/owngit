//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/state"
)

// Backup takes its lock file only as a file of this account with one name;
// a link planted there leaves the file it leads to as it was.
func TestBackupLockChangesNoFileAtItsName(t *testing.T) {
	stateDir := t.TempDir()
	noErr(t, os.WriteFile(filepath.Join(stateDir, "owngit.sqlite"), nil, 0o600))
	target := plantedProgram(t)
	noErr(t, os.Symlink(target, filepath.Join(stateDir, ".offline-operation.lock")))
	if err := backupState([]string{"--state-dir", stateDir, "--output", filepath.Join(t.TempDir(), "backup")}); err == nil {
		t.Error("backup went ahead")
	}
	requirePlantedProgram(t, target)
}

// Root's backup of another account's state is refused before its lock
// file is created there.
func TestRootBackupLeavesNoLockInAnotherAccountsState(t *testing.T) {
	home, _ := otherAccountsHome(t)
	stateDir := filepath.Join(home, "state")
	noErr(t, os.Mkdir(stateDir, 0o700))
	noErr(t, os.WriteFile(filepath.Join(stateDir, "owngit.sqlite"), nil, 0o600))
	noErr(t, os.Chown(filepath.Join(stateDir, "owngit.sqlite"), nobody, nobody))
	noErr(t, os.Chown(stateDir, nobody, nobody))
	err := backupState([]string{"--state-dir", stateDir, "--output", filepath.Join(t.TempDir(), "backup")})
	var other *state.OtherAccountError
	if !errors.As(err, &other) {
		t.Errorf("backup error=%v, want the state refused as another account's", err)
	}
	if _, err := os.Lstat(filepath.Join(stateDir, ".offline-operation.lock")); !os.IsNotExist(err) {
		t.Fatalf("backup left its lock file in the other account's state: %v", err)
	}
}

const nobody = 65534

// otherAccountsHome returns, for a test run by root, a folder of the
// account nobody and a folder of root's beside it.
func otherAccountsHome(t *testing.T) (home, roots string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	noErr(t, err)
	noErr(t, os.Chmod(base, 0o755))
	home, roots = filepath.Join(base, "home"), filepath.Join(base, "roots")
	noErr(t, os.Mkdir(home, 0o755))
	noErr(t, os.Chown(home, nobody, nobody))
	noErr(t, os.Mkdir(roots, 0o700))
	return home, roots
}

// plantedProgram is a file that a planted link or name leads to.
func plantedProgram(t *testing.T) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "program")
	noErr(t, os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755))
	noErr(t, os.Chmod(target, 0o755))
	return target
}

func requirePlantedProgram(t *testing.T, target string) {
	t.Helper()
	info, err := os.Stat(target)
	noErr(t, err)
	content, err := os.ReadFile(target)
	noErr(t, err)
	if info.Mode().Perm() != 0o755 || string(content) != "#!/bin/sh\n" {
		t.Fatalf("the planted file changed: mode %v, content %q", info.Mode().Perm(), content)
	}
}
