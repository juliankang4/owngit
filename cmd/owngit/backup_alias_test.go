package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/recovery"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

func aliasBackupState(t *testing.T, root string, alias bool) string {
	t.Helper()
	ctx := context.Background()
	stateDir := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	defer store.Close()
	hash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, "open", "", hash, false))
	runner, err := gitexec.New("", filepath.Join(stateDir, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	_, err = manager.Create(ctx, "project", "")
	noErr(t, err)
	remote, err := manager.Path("project")
	noErr(t, err)
	tree := prGitOutput(t, remote, "--git-dir", ".", "mktree")
	oid := prGitOutput(t, remote, "--git-dir", ".", "-c", "user.name=Backup Test", "-c", "user.email=backup@example.invalid", "commit-tree", tree, "-m", "data")
	runPRGit(t, remote, "--git-dir", ".", "update-ref", "refs/heads/main", oid)
	if alias {
		runPRGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	}
	return stateDir
}

func TestOfflineBackupReportsAliasAndReconnectCommand(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		root := t.TempDir()
		stateDir := aliasBackupState(t, root, true)
		backup := filepath.Join(root, "backup")
		args := []string{"--state-dir", stateDir, "--output", backup}
		if asJSON {
			args = append(args, "--json")
		}
		output, err := captureStdout(func() error { return backupState(args) })
		noErr(t, err)
		text := output
		if asJSON {
			var result offlineBackupResult
			noErr(t, json.Unmarshal([]byte(output), &result))
			if !result.OK || len(result.Warnings) != 1 || result.Note != "SHA-256 hashes detect corruption but do not authenticate a replaced backup." {
				t.Fatalf("JSON backup result: %s", output)
			}
			text = result.Warnings[0]
		}
		for _, want := range []string{recovery.AliasBranchNotice, "project: refs/heads/alias -> refs/heads/main", "git symbolic-ref -- 'refs/heads/alias' 'refs/heads/main'"} {
			if !strings.Contains(text, want) {
				t.Fatalf("offline backup lacks %q: %s", want, text)
			}
		}
		verified, err := captureStdout(func() error { return verifyBackup([]string{backup, "--json", "--temp-dir", root}) })
		noErr(t, err)
		if strings.Contains(verified, "alias_branches") || strings.Contains(verified, "symbolic-ref") {
			t.Fatalf("verification invented alias metadata: %s", verified)
		}
		stateTarget, repoTarget := filepath.Join(root, "restored-state"), filepath.Join(root, "restored-repositories")
		restored, err := captureStdout(func() error {
			return restoreState([]string{"--input", backup, "--state-dir", stateTarget, "--repository-root", repoTarget, "--json"})
		})
		noErr(t, err)
		var result restoreResult
		noErr(t, json.Unmarshal([]byte(restored), &result))
		if !result.OK || strings.Join(result.Notes, "\n") != strings.Join(recovery.RestoreNotes(), "\n") {
			t.Fatalf("restore changed its established notes: %s", restored)
		}
	}
}

func TestUpgradeBackupReportsAliasesInLogAndNote(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	remote := filepath.Join(filepath.Dir(stateDir), "repositories", testfixture.BaselineRepositoryID+".git")
	runPRGit(t, remote, "--git-dir", ".", "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	lines, err := openStateForTest(t, stateDir)
	noErr(t, err)
	backups := upgradeBackups(t, stateDir)
	if len(backups) != 1 {
		t.Fatalf("upgrade backups: %v", backups)
	}
	note, err := os.ReadFile(filepath.Join(state.UpgradeBackupFolder(stateDir), backups[0], upgradeNoteName))
	noErr(t, err)
	for _, text := range []string{strings.Join(lines, "\n"), string(note)} {
		for _, want := range []string{recovery.AliasBranchNotice, testfixture.BaselineRepositoryID + ": refs/heads/alias -> refs/heads/main", "git symbolic-ref -- 'refs/heads/alias' 'refs/heads/main'"} {
			if !strings.Contains(text, want) {
				t.Fatalf("upgrade backup result lacks %q: %s", want, text)
			}
		}
	}
}

func TestOfflineBackupWithoutAliasesKeepsItsOutput(t *testing.T) {
	root := t.TempDir()
	stateDir := aliasBackupState(t, root, false)
	backup := filepath.Join(root, "backup")
	output, err := captureStdout(func() error { return backupState([]string{"--state-dir", stateDir, "--output", backup, "--json"}) })
	noErr(t, err)
	encoded, err := json.Marshal(backup)
	noErr(t, err)
	want := "{\"ok\":true,\"backup\":" + string(encoded) + ",\"note\":\"SHA-256 hashes detect corruption but do not authenticate a replaced backup.\"}\n"
	if output != want {
		t.Fatalf("ordinary backup output changed: %q, want %q", output, want)
	}
}
