package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/apiclient"
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
