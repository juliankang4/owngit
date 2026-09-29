package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"owngit/internal/state"
)

// settings set changes only the settings it names, through the owner API,
// and settings show prints them as the server saved them.
func TestSettingsCommandSetsAndShows(t *testing.T) {
	fixture := startImportCLIServer(t)
	passwordPath := writePrivateTestFile(t, filepath.Join(t.TempDir(), "admin"), "admin-password\n")
	remote := []string{"--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath}
	if err := settingsCommand(append([]string{"set"}, remote...)); err == nil {
		t.Fatal("settings set without a setting was accepted")
	}
	if _, err := captureStdout(func() error {
		return settingsCommand(append([]string{"set", "--session", "7d"}, remote...))
	}); err != nil {
		t.Fatalf("settings set: %v", err)
	}
	printed, err := captureStdout(func() error { return settingsCommand(append([]string{"show"}, remote...)) })
	if err != nil {
		t.Fatalf("settings show: %v", err)
	}
	var shown struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal([]byte(printed), &shown); err != nil || shown.Settings["session"] != "7d" {
		t.Fatalf("settings show printed %q (%v)", printed, err)
	}
	if saved, err := fixture.store.GeneralSession(context.Background()); err != nil || saved != state.Session7Days {
		t.Fatalf("saved=%q err=%v", saved, err)
	}
}
