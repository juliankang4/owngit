package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/state"
)

func runReadCommandJSON(t *testing.T, command func([]string) error, arguments []string, result any) {
	t.Helper()
	output, err := captureStdout(func() error { return command(arguments) })
	noErr(t, err)
	if err := json.Unmarshal([]byte(output), result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
}

func TestActivityTasksAndCredentialListCommands(t *testing.T) {
	serverURL, passwordFile := startRepositoryCLIServer(t, "shared-password")
	remote := []string{"--server", serverURL, "--accept-insecure-http", "--password-file", passwordFile}
	noErr(t, repoCommand(append([]string{"create", "--name", "tools"}, remote...)))

	var activity struct {
		OK              bool  `json:"ok"`
		RepositoryCount int   `json:"repository_count"`
		Total           int   `json:"total"`
		Entries         []any `json:"entries"`
		Truncated       bool  `json:"truncated"`
	}
	runReadCommandJSON(t, activityCommand, append([]string{"--year", "2026"}, remote...), &activity)
	if !activity.OK || activity.RepositoryCount != 1 || activity.Total != 0 || len(activity.Entries) != 0 || activity.Truncated {
		t.Fatalf("activity=%+v", activity)
	}
	if code := commandErrorCode(activityCommand(append([]string{"--date", "2026-13-01"}, remote...))); code != "invalid_request" {
		t.Fatalf("activity with an invalid date: code=%q", code)
	}
	if code := commandErrorCode(activityCommand([]string{"--server", serverURL, "--accept-insecure-http"})); code != "authentication_required" {
		t.Fatalf("activity without the password: code=%q", code)
	}

	var tasks struct {
		OK    bool  `json:"ok"`
		Tasks []any `json:"tasks"`
	}
	for _, arguments := range [][]string{remote, append([]string{"--repository", "tools"}, remote...)} {
		runReadCommandJSON(t, tasksCommand, arguments, &tasks)
		if !tasks.OK || tasks.Tasks == nil || len(tasks.Tasks) != 0 {
			t.Fatalf("tasks %v: %+v", arguments, tasks)
		}
	}
	if code := commandErrorCode(tasksCommand(append([]string{"--task", "0123"}, remote...))); code != "invalid_arguments" {
		t.Fatalf("tasks --task without --repository: code=%q", code)
	}
	if code := commandErrorCode(tasksCommand(append([]string{"--repository", "tools", "--task", "0123"}, remote...))); code != "task_not_found" {
		t.Fatalf("missing task: code=%q", code)
	}

	adminPasswordFile := filepath.Join(t.TempDir(), "admin-password")
	noErr(t, os.WriteFile(adminPasswordFile, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminPasswordFile, false))
	admin := []string{"--server", serverURL, "--accept-insecure-http", "--password-file", adminPasswordFile}
	noErr(t, helperCredentialCommand(append([]string{"create", "--repository", "tools", "--label", "laptop", "--output", filepath.Join(t.TempDir(), "token")}, admin...)))
	var credentials struct {
		OK          bool `json:"ok"`
		Credentials []struct {
			RepositoryID string `json:"repository_id"`
			Label        string `json:"label"`
		} `json:"credentials"`
	}
	runReadCommandJSON(t, helperCredentialCommand, append([]string{"list"}, admin...), &credentials)
	if !credentials.OK || len(credentials.Credentials) != 1 || credentials.Credentials[0].RepositoryID != "tools" || credentials.Credentials[0].Label != "laptop" {
		t.Fatalf("every repository's credentials: %+v", credentials)
	}
}
