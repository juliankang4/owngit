package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/apiclient"
	"owngit/internal/auth"
)

// errorCode is the API error code of a failed command, or "".
func errorCode(err error) string {
	var problem *apiclient.Error
	if errors.As(err, &problem) {
		return problem.Code
	}
	return ""
}

// commandJSON runs command and decodes what it printed.
func commandJSON(t *testing.T, command func() error) map[string]any {
	t.Helper()
	printed, err := captureStdout(command)
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}
	var answer map[string]any
	if err := json.Unmarshal([]byte(printed), &answer); err != nil {
		t.Fatalf("printed %q: %v", printed, err)
	}
	return answer
}

// The owner's passwords, the shared access mode, the confirmation choice and
// the update check change from the command line as in Settings. New
// passwords come from files, never from arguments.
func TestSettingsCommandsChangeAccessAndPasswords(t *testing.T) {
	fixture := startImportCLIServer(t)
	directory := t.TempDir()
	adminPath := writePrivateTestFile(t, filepath.Join(directory, "admin"), "admin-password\n")
	remote := []string{"--server", fixture.url, "--accept-insecure-http", "--password-file", adminPath}

	sharedPath := writePrivateTestFile(t, filepath.Join(directory, "shared"), "new-shared-password\n")
	answer := commandJSON(t, func() error {
		return settingsCommand(append([]string{"access", "--mode", "password", "--access-password-file", sharedPath}, remote...))
	})
	if answer["access_mode"] != "password" || answer["changed"] != true {
		t.Fatalf("settings access printed %v", answer)
	}
	accessHash, err := fixture.store.PasswordHash(context.Background(), "access")
	noErr(t, err)
	if !auth.CheckPassword(accessHash, "new-shared-password") {
		t.Fatal("the shared password was not saved")
	}
	sameAsAdmin := writePrivateTestFile(t, filepath.Join(directory, "same"), "admin-password\n")
	if err := settingsCommand(append([]string{"access", "--mode", "password", "--access-password-file", sameAsAdmin}, remote...)); errorCode(err) != "invalid_password" {
		t.Fatalf("the administrator password as the shared password: %v", err)
	}
	if err := settingsCommand(append([]string{"access", "--mode", "open", "--access-password-file", sharedPath}, remote...)); errorCode(err) != "invalid_arguments" {
		t.Fatalf("open with a password: %v", err)
	}
	if answer := commandJSON(t, func() error { return settingsCommand(append([]string{"access", "--mode", "open"}, remote...)) }); answer["access_mode"] != "open" {
		t.Fatalf("settings access --mode open printed %v", answer)
	}

	if err := settingsCommand(append([]string{"confirmation", "--choice", "never"}, remote...)); errorCode(err) != "acknowledgement_required" {
		t.Fatalf("Do not ask without the acknowledgement: %v", err)
	}
	if answer := commandJSON(t, func() error {
		return settingsCommand(append([]string{"confirmation", "--choice", "never", "--acknowledge-no-ask"}, remote...))
	}); answer["admin_confirmation"] != "never" || answer["warnings"] == nil {
		t.Fatalf("settings confirmation printed %v", answer)
	}
	if answer := commandJSON(t, func() error { return settingsCommand(append([]string{"set", "--update-check", "off"}, remote...)) }); answer["settings"].(map[string]any)["update_check"] != "off" {
		t.Fatalf("settings set --update-check printed %v", answer)
	}
	shown := commandJSON(t, func() error { return settingsCommand(append([]string{"show"}, remote...)) })
	if shown["access_mode"] != "open" || shown["admin_confirmation"] != "never" || shown["settings"].(map[string]any)["update_check"] != "off" {
		t.Fatalf("settings show printed %v", shown)
	}

	newAdmin := writePrivateTestFile(t, filepath.Join(directory, "new-admin"), "new-admin-password\n")
	if answer := commandJSON(t, func() error {
		return settingsCommand(append([]string{"admin-password", "--new-password-file", newAdmin}, remote...))
	}); answer["ok"] != true {
		t.Fatalf("settings admin-password printed %v", answer)
	}
	if err := settingsCommand(append([]string{"show"}, remote...)); errorCode(err) != "invalid_admin_credentials" {
		t.Fatalf("the old administrator password still works: %v", err)
	}
	if _, err := captureStdout(func() error {
		return settingsCommand([]string{"show", "--server", fixture.url, "--accept-insecure-http", "--password-file", newAdmin})
	}); err != nil {
		t.Fatalf("the new administrator password is refused: %v", err)
	}
	// Without a file and without a terminal, nothing is read from standard
	// input and nothing changes.
	if err := settingsCommand([]string{"admin-password", "--server", fixture.url, "--accept-insecure-http", "--password-file", newAdmin}); errorCode(err) != "invalid_arguments" {
		t.Fatalf("a new password without a file or a terminal: %v", err)
	}
}

// repo default-branch and repo delete answer as the repository Settings tab
// and delete page do, and delete never takes the repository from the origin
// remote.
func TestRepoDefaultBranchAndDeleteCommands(t *testing.T) {
	fixture := startImportCLIServer(t)
	adminPath := writePrivateTestFile(t, filepath.Join(t.TempDir(), "admin"), "admin-password\n")
	remote := []string{"--server", fixture.url, "--accept-insecure-http", "--password-file", adminPath}
	work := newClone(t, fixture.url+"/git/project.git")
	runPRGit(t, work, "-c", "user.name=Owner", "-c", "user.email=owner@example.invalid", "commit", "--allow-empty", "-m", "first")
	runPRGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/trunk")

	if err := repoCommand(append([]string{"default-branch", "--repository", "project", "--branch", "missing"}, remote...)); errorCode(err) != "branch_not_found" {
		t.Fatalf("a missing branch: %v", err)
	}
	answer := commandJSON(t, func() error {
		return repoCommand(append([]string{"default-branch", "--repository", "project", "--branch", "trunk"}, remote...))
	})
	if answer["default_branch"] != "trunk" || answer["repository"] != "project" {
		t.Fatalf("repo default-branch printed %v", answer)
	}

	t.Chdir(work)
	if err := repoCommand(append([]string{"delete", "--files", "delete", "--confirm-name", "project"}, remote...)); errorCode(err) != "invalid_arguments" {
		t.Fatalf("a deletion without --repository: %v", err)
	}
	if err := repoCommand(append([]string{"delete", "--repository", "project", "--files", "later"}, remote...)); errorCode(err) != "invalid_arguments" {
		t.Fatalf("an unknown --files: %v", err)
	}
	if err := repoCommand(append([]string{"delete", "--repository", "project", "--files", "delete", "--confirm-name", "other"}, remote...)); errorCode(err) != "name_mismatch" {
		t.Fatalf("a wrong name: %v", err)
	}
	if _, exists, err := fixture.store.Repository(context.Background(), "project"); err != nil || !exists {
		t.Fatalf("a refused deletion removed the repository: exists=%v err=%v", exists, err)
	}
	answer = commandJSON(t, func() error {
		return repoCommand(append([]string{"delete", "--repository", "project", "--files", "keep", "--confirm-name", "project"}, remote...))
	})
	kept, _ := answer["kept_path"].(string)
	if answer["ok"] != true || answer["mode"] != "keep_files" || !strings.Contains(kept, ".owngit-removed") {
		t.Fatalf("repo delete printed %v", answer)
	}
	if _, err := os.Stat(filepath.Join(kept, "HEAD")); err != nil {
		t.Fatalf("the kept files are missing: %v", err)
	}
	if _, exists, err := fixture.store.Repository(context.Background(), "project"); err != nil || exists {
		t.Fatalf("the repository still exists: %v", err)
	}
}
