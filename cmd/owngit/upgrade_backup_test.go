package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// openStateForTest opens the state like serve and backup, which hold the
// offline lock from their start, and returns what it reported.
func openStateForTest(t *testing.T, stateDir string) ([]string, error) {
	t.Helper()
	held, err := state.CreateDirectory(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	unlock, err := state.AcquireOfflineLockIn(held)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	var lines []string
	store, err := openStateIn(context.Background(), held, "", func(format string, args ...any) {
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
	command := "owngit restore --input " + quoteForShell(backup) + " --state-dir " + quoteForShell(resolved) + " --repository-root " + quoteForShell(backup+"-repositories")
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
func TestUpgradeBackupWithReleasedAbandonedMergePlans(t *testing.T) {
	fixtures := os.Getenv("OWNGIT_RELEASED_MERGE_FIXTURES")
	if fixtures == "" {
		t.Skip("requires synthetic older-schema state produced by a released binary")
	}
	for _, status := range []string{state.MergeIntentPreparing, state.MergeIntentPlanned, state.MergeIntentReady} {
		t.Run(status, func(t *testing.T) {
			// Copy the closed released database byte for byte. Its repository
			// root remains the original synthetic fixture, not a live repository.
			content, err := os.ReadFile(filepath.Join(fixtures, "upgrade", status, "state", "owngit.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			stateDir := filepath.Join(root, "state")
			if err := os.Mkdir(stateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stateDir, "owngit.sqlite"), content, 0o600); err != nil {
				t.Fatal(err)
			}
			lines, err := openStateForTest(t, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) == 0 || !strings.Contains(lines[0], "before upgrading it from schema 15 to 16") {
				t.Fatalf("actual pre-upgrade backup did not run: %q", lines)
			}
			backups := upgradeBackups(t, stateDir)
			if len(backups) != 1 {
				t.Fatalf("upgrade backups: %v", backups)
			}
			backup := filepath.Join(state.UpgradeBackupFolder(stateDir), backups[0])
			result, err := recovery.Verify(context.Background(), backup, root, "")
			if err != nil || !result.Verified {
				t.Fatalf("upgrade backup verification: %+v err=%v", result, err)
			}
			if err := recovery.Restore(context.Background(), backup, filepath.Join(root, "restored-state"), filepath.Join(root, "restored-repositories"), ""); err != nil {
				t.Fatal(err)
			}
		})
	}
}

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

// Only the backups that an upgrade of this state directory made are
// removed, once a newer one is complete; other folders beside them stay.
func TestUpgradeBackupRemovesOnlyItsOwnOlderBackups(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	folder := state.UpgradeBackupFolder(stateDir)
	_, release, err := state.OpenUpgradeBackupFolder(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	release()
	older := filepath.Join(folder, "pre-1.1.3-20260101T000000Z")
	manual := filepath.Join(folder, "pre-1.1.0-20260926T232321")
	for _, path := range []string{older, manual} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Backups are private, as recovery.Create makes them.
	if err := state.ProtectPrivatePath(older, true); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(older, upgradeNoteName), []byte("note\n\n"+upgradeNoteStatePrefix+resolved+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A backup of another state directory whose backup folder is a link to
	// the same place.
	other := filepath.Join(folder, "pre-1.1.3-20260102T000000Z")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, upgradeNoteName), []byte("note\n\n"+upgradeNoteStatePrefix+resolved+"-other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manual, "owngit.sqlite"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A backup whose note has a second name is not one this account's
	// upgrade wrote, whatever the note says.
	linked := filepath.Join(folder, "pre-1.1.3-20260103T000000Z")
	if err := os.Mkdir(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, upgradeNoteName), []byte(upgradeNoteStatePrefix+resolved+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(linked, upgradeNoteName), filepath.Join(folder, "second-name")); err != nil {
		t.Fatal(err)
	}
	kept := []string{filepath.Base(manual), filepath.Base(other), filepath.Base(linked), "second-name"}
	if runtime.GOOS != "windows" {
		// A link to a backup is not a backup in this folder.
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.Mkdir(elsewhere, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(elsewhere, upgradeNoteName), []byte(upgradeNoteStatePrefix+resolved+"\n"), 0o600); err != nil {
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

// A command beside a server never upgrades and never takes the offline
// lock for it: while another OwnGit holds the lock, as a starting server
// does during its backup, the command still only says what to do, and a
// server that starts next is not kept out.
func TestCommandBesideAServerNeverUpgrades(t *testing.T) {
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
	var lines []string
	_, err = openState(context.Background(), stateDir, func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) })
	if !errors.Is(err, errOlderSchema) || !strings.Contains(err.Error(), "start or restart this OwnGit once") || len(lines) != 0 {
		t.Fatalf("err=%v lines=%q", err, lines)
	}
	unlock()
	_, err = captureStderr(func() error {
		return runCommand("approve-host", []string{"--state-dir", stateDir, "owngit.example.test"})
	})
	if !errors.Is(err, errOlderSchema) {
		t.Fatalf("approve-host err=%v", err)
	}
	requireStateFiles(t, stateDir, before)
	if backups := upgradeBackups(t, stateDir); len(backups) != 0 {
		t.Fatalf("backups %q", backups)
	}
	instance := startServed(t, stateDir)
	instance.stop()
	if !strings.Contains(instance.log(), baselineUpgradeLine) {
		t.Fatalf("serve log:\n%s", instance.log())
	}
}

// A service that is still backing up when the wait for it ends is reported
// as such, not as a state that needs an upgrade.
func TestServiceWaitNamesAStartStillBackingUp(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	_, err := waitHealthy(stateDir, 0)
	if err == nil || !strings.Contains(err.Error(), "still backing up the state") {
		t.Fatalf("err=%v", err)
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
