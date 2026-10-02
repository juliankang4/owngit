package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/recovery"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

func TestOfflineBackupReportsMissingAliasRecreation(t *testing.T) {
	root := t.TempDir()
	stateDir := aliasBackupState(t, root, false)
	remote := filepath.Join(root, "repositories", "project.git")
	runPRGit(t, remote, "--git-dir", ".", "update-ref", "refs/heads/future/topic", "refs/heads/main")
	runPRGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/missing-target", "refs/heads/future")
	runPRGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/chained-missing", "refs/heads/missing-target")
	output, err := captureStdout(func() error {
		return backupState([]string{"--state-dir", stateDir, "--output", filepath.Join(root, "backup"), "--json"})
	})
	noErr(t, err)
	var result offlineBackupResult
	noErr(t, json.Unmarshal([]byte(output), &result))
	if !result.OK || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], recovery.MissingAliasBranchNotice) || strings.Contains(result.Warnings[0], recovery.AliasBranchNotice) || strings.Contains(result.Warnings[0], recovery.UnresolvedAliasBranchNotice) {
		t.Fatalf("missing aliases were misreported: %s", output)
	}
	for _, pair := range [][2]string{{"refs/heads/missing-target", "refs/heads/future"}, {"refs/heads/chained-missing", "refs/heads/missing-target"}} {
		if !strings.Contains(result.Warnings[0], "project: "+pair[0]+" -> "+pair[1]) || !strings.Contains(result.Warnings[0], "git symbolic-ref -- '"+pair[0]+"' '"+pair[1]+"'") {
			t.Fatalf("backup lacks the exact alias recreation instruction: %s", output)
		}
	}
}

func TestUpgradeBackupReportsDirectoryTargetAliasesInLogAndNote(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	remote := filepath.Join(filepath.Dir(stateDir), "repositories", testfixture.BaselineRepositoryID+".git")
	runPRGit(t, remote, "--git-dir", ".", "update-ref", "refs/heads/future/topic", "refs/heads/main")
	pairs := map[string]string{"refs/heads/directory-target": "refs/heads/future", "refs/heads/chained-directory": "refs/heads/directory-target"}
	for name, target := range pairs {
		runPRGit(t, remote, "--git-dir", ".", "symbolic-ref", name, target)
	}
	lines, err := openStateForTest(t, stateDir)
	noErr(t, err)
	backups := upgradeBackups(t, stateDir)
	if len(backups) != 1 {
		t.Fatalf("upgrade backups: %v", backups)
	}
	note, err := os.ReadFile(filepath.Join(state.UpgradeBackupFolder(stateDir), backups[0], upgradeNoteName))
	noErr(t, err)
	for _, text := range []string{strings.Join(lines, "\n"), string(note)} {
		if !strings.Contains(text, recovery.MissingAliasBranchNotice) || strings.Contains(text, recovery.UnresolvedAliasBranchNotice) {
			t.Fatalf("upgrade backup misstates missing directory targets: %s", text)
		}
		for name, target := range pairs {
			entry := testfixture.BaselineRepositoryID + ": " + name + " -> " + target + ". " + (recovery.AliasBranch{Name: name, Target: target}).ReconnectCommand()
			if !strings.Contains(text, entry) {
				t.Fatalf("upgrade backup lacks a complete recreation entry: %s", text)
			}
		}
	}
}
