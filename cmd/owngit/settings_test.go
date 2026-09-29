package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

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
		return settingsCommand(append([]string{"set", "--session", "7d", "--initial-branch", "trunk", "--transfer-size", "512MB", "--transfer-time", "2h", "--check-logs", "indefinite", "--kept-history", "off"}, remote...))
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
	if err := json.Unmarshal([]byte(printed), &shown); err != nil || shown.Settings["session"] != "7d" || shown.Settings["initial_branch"] != "trunk" {
		t.Fatalf("settings show printed %q (%v)", printed, err)
	}
	if saved, err := fixture.store.GeneralSession(context.Background()); err != nil || saved != state.Session7Days {
		t.Fatalf("saved=%q err=%v", saved, err)
	}
	if saved, err := fixture.store.InitialBranch(context.Background()); err != nil || saved != "trunk" {
		t.Fatalf("saved=%q err=%v", saved, err)
	}
	if saved, err := fixture.store.GitTransferLimits(context.Background()); err != nil || saved != (state.GitTransferLimits{MaximumBytes: 512 << 20, Operation: 2 * time.Hour}) {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	if saved, err := fixture.store.CheckLogRetention(context.Background()); err != nil || saved != state.KeepCheckLogs {
		t.Fatalf("saved=%q err=%v", saved, err)
	}
	if saved, err := fixture.store.KeptHistory(context.Background()); err != nil || saved {
		t.Fatalf("saved kept history=%v err=%v", saved, err)
	}
	if err := settingsCommand(append([]string{"set", "--transfer-size", "4gb"}, remote...)); err == nil {
		t.Fatal("a size without a known unit was accepted")
	}
}

// repo settings set changes only the choices it names, and repo settings
// show prints them with what the repository does now. Inside a clone the
// server and the repository come from its origin remote.
func TestRepoSettingsCommandSetsAndShows(t *testing.T) {
	fixture := startImportCLIServer(t)
	passwordPath := writePrivateTestFile(t, filepath.Join(t.TempDir(), "admin"), "admin-password\n")
	remote := []string{"--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath, "--repository", "project"}
	if err := repoCommand([]string{"settings", "show", "--accept-insecure-http", "--server", fixture.url, "--repository", "project"}); err == nil {
		t.Fatal("repo settings without an administrator password file was accepted")
	}
	for _, refused := range [][]string{
		{"set"}, {"set", "--protect-default-branch", "yes"}, {"show", "--kept-history", "on"},
	} {
		if err := repoCommand(append(append([]string{"settings"}, refused...), remote...)); err == nil {
			t.Fatalf("repo settings %v was accepted", refused)
		}
	}
	printed, err := captureStdout(func() error {
		return repoCommand(append([]string{"settings", "set", "--kept-history", "off", "--protect-default-branch", "on"}, remote...))
	})
	if err != nil {
		t.Fatalf("repo settings set: %v", err)
	}
	var answer struct {
		Settings map[string]any `json:"settings"`
		Warnings []string       `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(printed), &answer); err != nil || len(answer.Warnings) != 1 {
		t.Fatalf("repo settings set printed %q (%v)", printed, err)
	}
	if saved, err := fixture.store.RepositoryRefPolicy(context.Background(), "project"); err != nil || saved != (state.RepositoryRefPolicy{KeptHistory: state.KeptHistoryOff, ProtectDefaultBranch: true}) {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	// A password file sent to an inferred server names that server.
	namedPath := writePrivateTestFile(t, filepath.Join(t.TempDir(), "named"), "owngit-server: "+fixture.url+"\nadmin-password\n")
	t.Chdir(newClone(t, fixture.url+"/git/project.git"))
	printed, err = captureStdout(func() error {
		return repoCommand([]string{"settings", "show", "--accept-insecure-http", "--password-file", namedPath})
	})
	if err != nil {
		t.Fatalf("repo settings show: %v", err)
	}
	if err := json.Unmarshal([]byte(printed), &answer); err != nil || answer.Settings["kept_history"] != "off" || answer.Settings["kept_history_now"] != "off" || answer.Settings["protect_default_branch"] != true {
		t.Fatalf("repo settings show printed %q (%v)", printed, err)
	}
}
