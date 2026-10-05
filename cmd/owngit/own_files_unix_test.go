//go:build !windows

package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"owngit/internal/state"
)

// The serve error goes into the state directory that was checked, even
// while the state directory's name is switched between that directory and
// a folder of another account (root's /tmp, which this account may write).
func TestServeErrorStaysInTheDirectoryThatWasChecked(t *testing.T) {
	if testing.Short() {
		t.Skip("writes for 5 seconds while the state directory is swapped")
	}
	if os.Geteuid() == 0 {
		t.Skip("needs an account that /tmp does not belong to")
	}
	other, err := filepath.EvalSymlinks("/tmp")
	noErr(t, err)
	info, err := os.Stat(other)
	noErr(t, err)
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != 0 || info.Mode().Perm()&0o002 == 0 {
		t.Skipf("%s is not root's folder that every account may write", other)
	}
	stray := filepath.Join(other, serveErrorFile)
	if _, err := os.Lstat(stray); err == nil {
		t.Skipf("%s exists already", stray)
	}
	own, links := t.TempDir(), t.TempDir()
	link := filepath.Join(links, "state")
	noErr(t, os.Symlink(own, link))
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		next := filepath.Join(links, "next")
		for turn := 0; ; turn++ {
			select {
			case <-stop:
				return
			default:
			}
			target := own
			if turn%2 == 1 {
				target = other
			}
			if os.Symlink(target, next) == nil {
				_ = os.Rename(next, link)
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	deadline := time.Now().Add(5 * time.Second)
	for attempt := 1; attempt <= 20000 && time.Now().Before(deadline); attempt++ {
		recordServeError(link, errors.New("synthetic failure"))
		if _, err := os.Lstat(stray); err == nil {
			t.Fatalf("attempt %d wrote the serve error into %s (move that file away before running this test again)", attempt, other)
		}
	}
	if _, err := os.Lstat(filepath.Join(own, serveErrorFile)); err != nil {
		t.Fatalf("the serve error never reached the checked directory: %v", err)
	}
}

// A link at the log's name is refused before anything is written, so the
// file it leads to keeps its content and mode.
func TestLogIsNotOpenedThroughALink(t *testing.T) {
	target := plantedProgram(t)
	path := filepath.Join(t.TempDir(), "owngit.log")
	noErr(t, os.Symlink(target, path))
	previous := log.Writer()
	closeLog, err := writeLogTo(path, true)
	if err == nil {
		closeLog()
		log.SetOutput(previous)
		t.Error("the log opened through a link")
	}
	requirePlantedProgram(t, target)
}

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

// When root runs serve for another account's state, a link that account
// put at the state directory's name must not lead the serve error into a
// folder of root's.
func TestRootRecordsNoServeErrorThroughAnotherAccountsLink(t *testing.T) {
	home, roots := otherAccountsHome(t)
	kept := filepath.Join(roots, serveErrorFile)
	noErr(t, os.WriteFile(kept, []byte("kept\n"), 0o600))
	link := filepath.Join(home, "state")
	noErr(t, os.Symlink(roots, link))
	noErr(t, os.Lchown(link, nobody, nobody))
	recordServeError(link, errors.New("synthetic failure"))
	if content, err := os.ReadFile(kept); err != nil || string(content) != "kept\n" {
		t.Fatalf("root's file behind the link now holds %q (%v)", content, err)
	}
}

// Root does not open a log in another account's folder: the log would be
// root's there, and a link that account put at its name would lead root's
// writes elsewhere.
func TestRootOpensNoLogInAnotherAccountsFolder(t *testing.T) {
	home, _ := otherAccountsHome(t)
	target := plantedProgram(t)
	path := filepath.Join(home, "service.log")
	noErr(t, os.Symlink(target, path))
	noErr(t, os.Lchown(path, nobody, nobody))
	previous := log.Writer()
	closeLog, err := writeLogTo(path, true)
	if err == nil {
		closeLog()
		log.SetOutput(previous)
	}
	var other *state.OtherAccountError
	if !errors.As(err, &other) {
		t.Errorf("writeLogTo error=%v, want the folder refused as another account's", err)
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
