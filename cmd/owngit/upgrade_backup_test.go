package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/recovery"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

// openStateForTest opens the state like a command next to a server and
// returns what it reported.
func openStateForTest(t *testing.T, stateDir string) ([]string, error) {
	t.Helper()
	var lines []string
	store, err := openState(context.Background(), stateDir, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	if err == nil {
		err = store.Close()
	}
	return lines, err
}

// upgradeBackups lists the backups in the state's upgrade backup folder.
func upgradeBackups(t *testing.T, stateDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(state.UpgradeBackupFolder(stateDir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// stateFiles reads every file in the state directory other than the
// offline lock, to show that a refused upgrade changed nothing there.
func stateFiles(t *testing.T, stateDir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, entry := range entries {
		if entry.Name() == ".offline-operation.lock" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(stateDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = content
	}
	return files
}

func requireStateFiles(t *testing.T, stateDir string, want map[string][]byte) {
	t.Helper()
	got := stateFiles(t, stateDir)
	if len(got) != len(want) {
		t.Fatalf("state directory entries changed: %d, want %d", len(got), len(want))
	}
	for name, content := range want {
		if !bytes.Equal(got[name], content) {
			t.Fatalf("%s changed", name)
		}
	}
}

// The backup before an upgrade is a backup of the state as it was, in the
// format the previous release restores, with a note that says how.
func TestUpgradeBackupRestoresTheStateBeforeTheUpgrade(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	lines, err := openStateForTest(t, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	backups := upgradeBackups(t, stateDir)
	if len(backups) != 1 || !strings.HasPrefix(backups[0], "pre-") {
		t.Fatalf("upgrade backups %q", backups)
	}
	resolved, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(state.UpgradeBackupFolder(resolved), backups[0])
	if want := "backed up the state to " + backup + " before upgrading it from the committed baseline"; !strings.HasPrefix(lines[0], want) {
		t.Fatalf("reported %q, want %q", lines[0], want)
	}
	command := "owngit restore --input " + quoteForShell(backup) + " --state-dir " + quoteForShell(resolved)
	if !strings.Contains(lines[1], command) {
		t.Fatalf("reported %q, want the restore command %q", lines[1], command)
	}
	note, err := os.ReadFile(filepath.Join(backup, upgradeNoteName))
	if err != nil || !strings.Contains(string(note), command) {
		t.Fatalf("note %q err=%v", note, err)
	}

	// The previous release reads backup format 10. When the format
	// changes, the backup before an upgrade must still be written in the
	// format that the release before it restores.
	content, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(content, &manifest); err != nil || manifest.Version != 10 {
		t.Fatalf("backup format %d err=%v, want 10", manifest.Version, err)
	}

	root := t.TempDir()
	restoredState, restoredRepositories := filepath.Join(root, "state"), filepath.Join(root, "repositories")
	if err := recovery.Restore(context.Background(), backup, restoredState, restoredRepositories, ""); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), restoredState)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository, found, err := store.Repository(context.Background(), testfixture.BaselineRepositoryID)
	if err != nil || !found || repository.Name != "Baseline project" {
		t.Fatalf("restored repository %+v found=%v err=%v", repository, found, err)
	}
	if _, err := os.Stat(filepath.Join(restoredRepositories, testfixture.BaselineRepositoryID+".git")); err != nil {
		t.Fatal(err)
	}
}

// When the backup cannot be made, the state is not upgraded: its files stay
// as they were, so the earlier version can still use it, and the error
// says what to do. The next start after the cause is fixed upgrades it.
func TestFailedUpgradeBackupLeavesTheStateAsItWas(t *testing.T) {
	for _, test := range []struct {
		name    string
		breakIt func(t *testing.T, stateDir string) (repair func())
	}{
		{"backup folder unusable", func(t *testing.T, stateDir string) func() {
			folder := state.UpgradeBackupFolder(stateDir)
			if err := os.WriteFile(folder, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.Remove(folder); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"repository folder missing", func(t *testing.T, stateDir string) func() {
			repositories := filepath.Join(filepath.Dir(stateDir), "repositories")
			moved := repositories + "-away"
			if err := os.Rename(repositories, moved); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.Rename(moved, repositories); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateDir := createBaselineStateForTest(t)
			before := stateFiles(t, stateDir)
			repair := test.breakIt(t, stateDir)
			lines, err := openStateForTest(t, stateDir)
			if err == nil || !strings.Contains(err.Error(), "the state was not upgraded from the committed baseline") || !strings.Contains(err.Error(), "owngit upgrade-backup off") {
				t.Fatalf("err=%v", err)
			}
			if len(lines) != 0 {
				t.Fatalf("reported %q", lines)
			}
			requireStateFiles(t, stateDir, before)
			repair()
			if lines, err := openStateForTest(t, stateDir); err != nil || len(lines) != 3 {
				t.Fatalf("after the repair: lines=%q err=%v", lines, err)
			}
		})
	}
}

// With the backup turned off, the upgrade happens without one and says so.
func TestUpgradeWithTheBackupOff(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	output, err := captureStdout(func() error {
		return runCommand("upgrade-backup", []string{"off", "--state-dir", stateDir})
	})
	if err != nil || !strings.HasPrefix(output, "Warning: ") {
		t.Fatalf("turning the backup off printed %q", output)
	}
	lines, err := openStateForTest(t, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "without a backup") || lines[1] != baselineUpgradeLine {
		t.Fatalf("reported %q", lines)
	}
	if backups := upgradeBackups(t, stateDir); len(backups) != 0 {
		t.Fatalf("backups %q were made with the backup off", backups)
	}
	output, err = captureStdout(func() error {
		return runCommand("upgrade-backup", []string{"on", "--state-dir", stateDir, "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Enabled bool   `json:"enabled"`
		Folder  string `json:"folder"`
	}
	if err := json.Unmarshal([]byte(output), &status); err != nil || !status.Enabled || filepath.Base(status.Folder) != "state-backups" {
		t.Fatalf("status %q err=%v", output, err)
	}
}

// Only the backups that an upgrade made are removed, once a newer one is
// complete; other folders beside them stay.
func TestUpgradeBackupRemovesOnlyItsOwnOlderBackups(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	folder := state.UpgradeBackupFolder(stateDir)
	older := filepath.Join(folder, "pre-1.1.3-20260101T000000Z")
	manual := filepath.Join(folder, "pre-1.1.0-20260926T232321")
	for _, path := range []string{older, manual} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(older, upgradeNoteName), []byte("note"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manual, "owngit.sqlite"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	kept := []string{filepath.Base(manual)}
	if runtime.GOOS != "windows" {
		// A link to a backup is not a backup in this folder.
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.Mkdir(elsewhere, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(elsewhere, upgradeNoteName), []byte("note"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(folder, "link")); err != nil {
			t.Fatal(err)
		}
		kept = append(kept, "link")
	}
	if _, err := openStateForTest(t, stateDir); err != nil {
		t.Fatal(err)
	}
	backups := upgradeBackups(t, stateDir)
	for _, name := range kept {
		if !strings.Contains(strings.Join(backups, "\n"), name) {
			t.Fatalf("%s was removed: %q", name, backups)
		}
	}
	if len(backups) != len(kept)+1 {
		t.Fatalf("backups %q, want %q and the new one", backups, kept)
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Fatalf("the older upgrade backup stayed: %v", err)
	}
}

// A command next to a server upgrades only with the offline lock, so it
// never upgrades a state that another OwnGit uses.
func TestCommandUpgradesOnlyWithTheOfflineLock(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	held, err := state.OpenStateDirectory(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	unlock, err := state.AcquireOfflineLockIn(held)
	if err != nil {
		t.Fatal(err)
	}
	before := stateFiles(t, stateDir)
	if _, err := openStateForTest(t, stateDir); err == nil || !strings.Contains(err.Error(), "only while no other OwnGit uses it") {
		t.Fatalf("err=%v", err)
	}
	requireStateFiles(t, stateDir, before)
	if backups := upgradeBackups(t, stateDir); len(backups) != 0 {
		t.Fatalf("backups %q", backups)
	}
	unlock()
	if _, err := openStateForTest(t, stateDir); err != nil {
		t.Fatal(err)
	}
}

// serve backs up an older state before it upgrades it and says so in its
// log; when the backup fails, it does not start, and the state stays as it
// was.
func TestServeBacksUpAnOlderStateBeforeItUpgradesIt(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	folder := state.UpgradeBackupFolder(stateDir)
	if err := os.WriteFile(folder, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before := stateFiles(t, stateDir)
	err := serveWithContext(context.Background(), []string{"--state-dir", stateDir, "--listen", "127.0.0.1:0", "--no-open"}, func(string) error { return nil }, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "the state was not upgraded") {
		t.Fatalf("serve err=%v", err)
	}
	requireStateFiles(t, stateDir, before)
	if err := os.Remove(folder); err != nil {
		t.Fatal(err)
	}
	instance := startServed(t, stateDir)
	instance.stop()
	log := instance.log()
	if !strings.Contains(log, "backed up the state to ") || !strings.Contains(log, "owngit restore --input ") || !strings.Contains(log, baselineUpgradeLine) {
		t.Fatalf("serve log:\n%s", log)
	}
}
