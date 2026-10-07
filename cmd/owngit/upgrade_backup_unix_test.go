//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/state"
)

// A backup folder that other accounts can write to, sticky or not, is
// refused before anything is written, and the state stays as it was.
func TestSharedBackupFolderIsRefused(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	folder := state.UpgradeBackupFolder(stateDir)
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(folder, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	before := stateFiles(t, stateDir)
	_, err := openStateForTest(t, stateDir)
	if err == nil || !strings.Contains(err.Error(), "the state was not upgraded") || !strings.Contains(err.Error(), "another account can change") || !strings.Contains(err.Error(), "chmod g-w,o-w ") || !strings.Contains(err.Error(), "only this account can change") || !strings.Contains(err.Error(), "move it away") {
		t.Fatalf("err=%v", err)
	}
	requireStateFiles(t, stateDir, before)
	if entries, err := os.ReadDir(folder); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

// Root's upgrade never removes another account's folder. With a shared
// backup folder, as in the reported case (a root folder with mode 01777
// in which one account planted a note in another account's folder), the
// upgrade is refused; in a private backup folder, a foreign folder or a
// foreign note with a valid last line stays, while root's own older backup
// is removed. Runs only as root.
func TestRootNeverRemovesAnotherAccountsFolder(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to own files as other accounts")
	}
	const alice, mallory = 4242, 4343
	stateDir := createBaselineStateForTest(t)
	resolved, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	folder := state.UpgradeBackupFolder(stateDir)
	note := []byte("OwnGit made this backup.\n\n" + upgradeNoteStatePrefix + resolved + "\n")
	plant := func(name string, dirOwner, noteOwner int, mode os.FileMode) string {
		t.Helper()
		victim := filepath.Join(folder, name)
		secret := filepath.Join(victim, "protected", "secret")
		for _, step := range []error{
			os.MkdirAll(filepath.Dir(secret), 0o700), os.WriteFile(secret, []byte("alice"), 0o600),
			os.Chown(filepath.Dir(secret), alice, alice), os.Chown(secret, alice, alice),
			os.WriteFile(filepath.Join(victim, upgradeNoteName), note, 0o600),
			os.Chown(filepath.Join(victim, upgradeNoteName), noteOwner, noteOwner),
			os.Chown(victim, dirOwner, dirOwner), os.Chmod(victim, mode),
		} {
			if step != nil {
				t.Fatal(step)
			}
		}
		return secret
	}

	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(folder, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	secret := plant("pre-1.1.3-20260101T000000Z", alice, mallory, 0o777|os.ModeSticky)
	before := stateFiles(t, stateDir)
	if _, err := openStateForTest(t, stateDir); err == nil || !strings.Contains(err.Error(), "only this account can change") {
		t.Fatalf("shared folder: err=%v", err)
	}
	requireStateFiles(t, stateDir, before)
	if content, err := os.ReadFile(secret); err != nil || string(content) != "alice" {
		t.Fatalf("alice's file: %q err=%v", content, err)
	}

	if err := os.Chmod(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	kept := []string{
		secret,
		plant("pre-1.1.3-20260102T000000Z", alice, alice, 0o700),
		plant("pre-1.1.3-20260103T000000Z", 0, mallory, 0o700),
	}
	own := filepath.Join(folder, "pre-1.1.3-20260104T000000Z")
	if err := os.Mkdir(own, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, upgradeNoteName), note, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openStateForTest(t, stateDir); err != nil {
		t.Fatal(err)
	}
	for _, path := range kept {
		if content, err := os.ReadFile(path); err != nil || string(content) != "alice" {
			t.Fatalf("%s: %q err=%v", path, content, err)
		}
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatalf("root's own older backup stayed: %v", err)
	}
}
