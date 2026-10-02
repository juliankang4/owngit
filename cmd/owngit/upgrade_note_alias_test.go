package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/recovery"
	"owngit/internal/state"
	"owngit/internal/testfixture"
	"owngit/internal/version"
)

func TestAliasUpgradeBackupIsRecognizedAndRetired(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	remote := filepath.Join(filepath.Dir(stateDir), "repositories", testfixture.BaselineRepositoryID+".git")
	runPRGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	_, err := openStateForTest(t, stateDir)
	noErr(t, err)
	canonical, err := filepath.EvalSymlinks(stateDir)
	noErr(t, err)
	names := upgradeBackups(t, canonical)
	if len(names) != 1 {
		t.Fatalf("upgrade backups: %v", names)
	}
	folder, release, err := state.OpenUpgradeBackupFolder(canonical)
	noErr(t, err)
	defer release()
	if !upgradeBackupOf(folder, names[0], canonical) {
		t.Fatal("alias-bearing upgrade backup was not recognized as owned")
	}
	removeOlderUpgradeBackups(folder, "later-backup", canonical, t.Logf)
	if _, err := os.Stat(filepath.Join(folder.Name(), names[0])); !os.IsNotExist(err) {
		t.Fatalf("the older owned alias backup was retained: %v", err)
	}
}

func TestUpgradeNoteKeepsOwnershipFooterAfterLongAliasNotice(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	canonical, err := filepath.EvalSymlinks(stateDir)
	noErr(t, err)
	folder, release, err := state.OpenUpgradeBackupFolder(canonical)
	noErr(t, err)
	defer release()
	upgrade := &state.Upgrade{From: 15, To: 16}
	for _, notice := range []string{"", recovery.AliasBranchNotice + strings.Repeat(" alias entry", 8000)} {
		name := "plain"
		if notice != "" {
			name = "long-alias"
		}
		backup := filepath.Join(folder.Name(), name)
		noErr(t, os.Mkdir(backup, 0o700))
		noErr(t, state.ProtectPrivatePath(backup, true))
		noErr(t, writeUpgradeNote(backup, canonical, upgrade, "owngit restore", notice))
		content, err := os.ReadFile(filepath.Join(backup, upgradeNoteName))
		noErr(t, err)
		if !strings.HasSuffix(string(content), "\n"+upgradeNoteStatePrefix+canonical+"\n") {
			t.Fatalf("ownership footer is not the last line in %s", name)
		}
		if notice == "" {
			want := fmt.Sprintf("OwnGit %s made this backup before it upgraded the state in %s %s.\n\n"+
				"To go back to the earlier OwnGit version, stop OwnGit, move %s aside, and run this with the earlier version:\n\n  %s\n\n"+
				"If the earlier version refuses this backup, keep using this OwnGit version; otherwise, start the earlier version. The restored repositories are in the folder after --repository-root; another new folder in a place this account can create works as well.\n"+
				"OwnGit removes this backup once it has made a newer one for this state directory before a later upgrade.\n\n"+
				upgradeNoteStatePrefix+"%s\n", version.Version, canonical, upgrade.Describe(), canonical, "owngit restore", canonical)
			if string(content) != want {
				t.Fatal("note bytes changed without aliases")
			}
		} else if len(content) <= 64<<10 {
			t.Fatal("fixture must exceed the old first-window bound")
		}
		if !upgradeBackupOf(folder, name, canonical) || upgradeBackupOf(folder, name, canonical+"-other") {
			t.Fatalf("note ownership recognition is incorrect for %s", name)
		}
	}
	removeOlderUpgradeBackups(folder, "later-backup", canonical, t.Logf)
	for _, name := range []string{"plain", "long-alias"} {
		if _, err := os.Stat(filepath.Join(folder.Name(), name)); !os.IsNotExist(err) {
			t.Fatalf("older %s backup remains: %v", name, err)
		}
	}
}
