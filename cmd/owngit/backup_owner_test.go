package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/apiclient"
	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// The backup commands of a running server print its JSON, and the MCP
// backup_status tool returns what owngit backup status prints.
func TestBackupCommandsTalkToTheServer(t *testing.T) {
	serverURL, _ := startRepositoryCLIServer(t, "")
	adminFile := filepath.Join(t.TempDir(), "admin-password")
	noErr(t, os.WriteFile(adminFile, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminFile, false))
	remote := []string{"--server", serverURL, "--accept-insecure-http", "--password-file", adminFile}
	command := func(arguments ...string) []string { return append(arguments, remote...) }

	status := cliOutput(t, backupState, command("status")...)
	if !strings.Contains(status, `"state":"not_configured"`) || !strings.Contains(status, `"last_run":null`) {
		t.Fatalf("status: %s", status)
	}
	session := startMCPSession(t, mcpOptions{server: serverURL, acceptInsecureHTTP: true})
	if text, isError := session.call("backup_status", nil); isError || text != status {
		t.Fatalf("backup_status isError=%v\n got %s\nwant %s", isError, text, status)
	}
	for _, refused := range []struct {
		arguments []string
		code      string
	}{
		{command("now"), "backup_not_configured"},
		{command("schedule", "set", "--keep", "3"), "invalid_backup_schedule"},
		{command("schedule", "set", "--destination", "relative"), "invalid_backup_schedule"},
	} {
		_, err := captureStdout(func() error { return backupState(refused.arguments) })
		var problem *apiclient.Error
		if !errors.As(err, &problem) || problem.Code != refused.code {
			t.Errorf("%v: %v", refused.arguments, err)
		}
	}
	if _, err := captureStdout(func() error { return backupState(command("schedule", "set", "--keep", "many")) }); err == nil {
		t.Error("--keep many was accepted")
	}

	destination := filepath.Join(t.TempDir(), "backups")
	var saved struct {
		Schedule struct {
			State       string `json:"state"`
			Destination string `json:"destination"`
			Interval    string `json:"interval"`
			Keep        int    `json:"keep"`
			Verify      bool   `json:"verify"`
		} `json:"schedule"`
		Warnings []string `json:"warnings"`
	}
	output := cliOutput(t, backupState, command("schedule", "set", "--destination", destination, "--interval", "12h", "--keep", "2", "--verify", "off")...)
	noErr(t, json.Unmarshal([]byte(output), &saved))
	if saved.Schedule.State != "on" || saved.Schedule.Destination != destination || saved.Schedule.Interval != "12h" || saved.Schedule.Keep != 2 ||
		saved.Schedule.Verify || len(saved.Warnings) != 1 {
		t.Fatalf("schedule set: %s", output)
	}
	output = cliOutput(t, backupState, command("schedule", "off")...)
	noErr(t, json.Unmarshal([]byte(output), &saved))
	if saved.Schedule.State != "off" || saved.Schedule.Keep != 2 || len(saved.Warnings) != 1 {
		t.Fatalf("schedule off: %s", output)
	}
	if shown := cliOutput(t, backupState, command("schedule", "show")...); !strings.Contains(shown, `"state":"off"`) {
		t.Fatalf("schedule show: %s", shown)
	}
	if runs := cliOutput(t, backupState, command("runs")...); !strings.HasPrefix(runs, `{"ok":true,"runs":[`) {
		t.Fatalf("runs: %s", runs)
	}
}

// A serving OwnGit records a backup that a stopped process left running as
// interrupted, does not repeat its scheduled slot, and makes a verified
// backup when asked.
func TestServeRunsBackups(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	repositoriesRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoriesRoot, 0o700))
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoriesRoot, "open", "", adminHash, true))
	runner, err := gitexec.New("", filepath.Join(stateDir, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	_, err = manager.Create(ctx, "project", "")
	noErr(t, err)
	destination := filepath.Join(root, "backups")
	noErr(t, store.SaveBackupSchedule(ctx, state.BackupSchedule{Enabled: true, Interval: 24 * time.Hour, Destination: destination, Keep: 7, Verify: true, UpdatedAt: time.Now()}))
	left := state.BackupRun{ID: strings.Repeat("d", 32), Kind: state.BackupRunScheduled, Destination: destination, BackupName: "owngit-backup-left", StartedAt: time.Now().Add(-time.Hour)}
	noErr(t, store.StartBackupRun(ctx, left))
	noErr(t, store.Close())

	instance := startServed(t, stateDir)
	instance.waitForLog("recorded as interrupted", time.Minute)
	adminFile := filepath.Join(root, "admin-password")
	noErr(t, os.WriteFile(adminFile, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(adminFile, false))
	remote := []string{"--server", instance.url, "--accept-insecure-http", "--password-file", adminFile}
	var started struct {
		Run struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"run"`
	}
	noErr(t, json.Unmarshal([]byte(cliOutput(t, backupState, append([]string{"now"}, remote...)...)), &started))
	instance.waitForLog("backup "+started.Run.Path+" ", 5*time.Minute)

	var status struct {
		LastRun struct {
			ID, Status, Verification string
			LongestHoldMS            *int64 `json:"longest_hold_ms"`
		} `json:"last_run"`
		LastVerified *struct{ ID string } `json:"last_verified"`
		NextRun      time.Time            `json:"next_run"`
	}
	output := cliOutput(t, backupState, append([]string{"status"}, remote...)...)
	noErr(t, json.Unmarshal([]byte(output), &status))
	if status.LastRun.ID != started.Run.ID || status.LastRun.Status != "succeeded" || status.LastRun.Verification != "passed" ||
		status.LastRun.LongestHoldMS == nil || status.LastVerified == nil || status.LastVerified.ID != started.Run.ID ||
		!status.NextRun.Equal(left.StartedAt.Truncate(time.Second).Add(24*time.Hour)) {
		t.Fatalf("status: %s", output)
	}
	var runs struct {
		Runs []struct{ ID, Status, Message string } `json:"runs"`
	}
	noErr(t, json.Unmarshal([]byte(cliOutput(t, backupState, append([]string{"runs"}, remote...)...)), &runs))
	if len(runs.Runs) != 2 || runs.Runs[1].ID != left.ID || runs.Runs[1].Status != "interrupted" {
		t.Fatalf("runs: %+v", runs)
	}
}
