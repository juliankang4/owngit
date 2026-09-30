package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
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
		return settingsCommand(append([]string{"set", "--session", "7d", "--initial-branch", "trunk", "--transfer-size", "512MB", "--transfer-time", "2h", "--transfer-per-repository", "2", "--transfer-queue", "2m", "--check-logs", "indefinite", "--kept-history", "off",
			"--browse-file", "4MB", "--browse-compare-time", "30s", "--maintenance-window", "22-6", "--maintenance-packs", "40", "--unused-object-cleanup", "on", "--cleanup-grace-days", "30"}, remote...))
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
	if saved, err := fixture.store.GitTransferLimits(context.Background()); err != nil || saved != (state.GitTransferLimits{MaximumBytes: 512 << 20, Operation: 2 * time.Hour, PerRepository: 2, ExtraSlots: 1, Idle: time.Minute, QueueWait: 2 * time.Minute}) {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	if saved, err := fixture.store.CheckLogRetention(context.Background()); err != nil || saved != state.KeepCheckLogs {
		t.Fatalf("saved=%q err=%v", saved, err)
	}
	if saved, err := fixture.store.KeptHistory(context.Background()); err != nil || saved {
		t.Fatalf("saved kept history=%v err=%v", saved, err)
	}
	if saved, err := fixture.store.BrowseLimits(context.Background()); err != nil || saved.FileBytes != 4<<20 || saved.CompareTime != 30*time.Second || saved.RawBytes != 10<<20 {
		t.Fatalf("saved browsing limits=%+v err=%v", saved, err)
	}
	if saved, err := fixture.store.Maintenance(context.Background()); err != nil || saved.WindowStart != 22 || saved.WindowEnd != 6 || saved.PackThreshold != 40 || !saved.Enabled {
		t.Fatalf("saved maintenance=%+v err=%v", saved, err)
	}
	if saved, err := fixture.store.UnusedObjectCleanup(context.Background()); err != nil || !saved.Enabled || saved.Grace != 30*24*time.Hour {
		t.Fatalf("saved cleanup=%+v err=%v", saved, err)
	}
	for _, refused := range [][]string{{"--maintenance-window", "3"}, {"--maintenance", "yes"}, {"--browse-raw", "10"}} {
		if err := settingsCommand(append(append([]string{"set"}, refused...), remote...)); err == nil {
			t.Fatalf("settings set %v was accepted", refused)
		}
	}
	if err := settingsCommand(append([]string{"set", "--transfer-size", "4gb"}, remote...)); err == nil {
		t.Fatal("a size without a known unit was accepted")
	}
}

// The access policies are set by name like every other setting, and the
// login limits keep the ones not named.
func TestSettingsCommandSetsTheAccessPolicies(t *testing.T) {
	fixture := startImportCLIServer(t)
	passwordPath := writePrivateTestFile(t, filepath.Join(t.TempDir(), "admin"), "admin-password\n")
	remote := []string{"--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath}
	printed, err := captureStdout(func() error {
		return settingsCommand(append([]string{"set", "--delete-requires-name", "off", "--cross-site-links", "lax", "--login-attempts", "6", "--login-pause", "30m"}, remote...))
	})
	if err != nil {
		t.Fatalf("settings set: %v", err)
	}
	var answer struct {
		Settings map[string]any `json:"settings"`
		Warnings []string       `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(printed), &answer); err != nil || answer.Settings["cross_site_links"] != "lax" || len(answer.Warnings) != 3 {
		t.Fatalf("settings set printed %q (%v)", printed, err)
	}
	ctx := context.Background()
	if saved, err := fixture.store.DeleteRequiresName(ctx); err != nil || saved {
		t.Fatalf("saved delete choice=%v err=%v", saved, err)
	}
	if saved, err := fixture.store.CrossSiteLinks(ctx); err != nil || saved != state.CrossSiteLax {
		t.Fatalf("saved cross-site choice=%q err=%v", saved, err)
	}
	if saved, err := fixture.store.LoginLimits(ctx); err != nil || saved != (state.LoginLimits{Attempts: 6, Window: 10 * time.Minute, Pause: 30 * time.Minute}) {
		t.Fatalf("saved login limits=%+v err=%v", saved, err)
	}
	for _, refused := range [][]string{{"--login-window", "90s500ms"}, {"--login-pause", "later"}, {"--login-attempts", "0"}, {"--cross-site-links", "none"}} {
		if _, err := captureStdout(func() error { return settingsCommand(append(append([]string{"set"}, refused...), remote...)) }); err == nil {
			t.Fatalf("settings set %v was accepted", refused)
		}
	}
}

// The command that unreadable login limits name, in the API advice and in
// the sign-in message of both languages, sets them again as it says.
func TestTheNamedCommandRepairsUnreadableLoginLimits(t *testing.T) {
	fixture := startImportCLIServer(t)
	passwordPath := writePrivateTestFile(t, filepath.Join(t.TempDir(), "admin"), "admin-password\n")
	remote := []string{"--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath}
	ctx := context.Background()
	corrupt := func() {
		t.Helper()
		if err := fixture.store.Exec(ctx, `INSERT INTO metadata(key,value) VALUES('login_limits','null') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
			t.Fatal(err)
		}
	}
	corrupt()
	_, err := fixture.store.LoginLimits(ctx)
	var policyErr *state.PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("stored null read with err=%v, want a PolicyError", err)
	}
	for _, advice := range []string{policyErr.Advice(), webui.Text(webui.LangEN, webui.MsgLoginLimitsUnreadable), webui.Text(webui.LangKO, webui.MsgLoginLimitsUnreadable)} {
		corrupt()
		start := strings.Index(advice, "owngit settings set ")
		end := strings.Index(advice[max(start, 0):], "(")
		if start < 0 || end < 0 {
			t.Fatalf("no command in %q", advice)
		}
		command := strings.Fields(advice[start+len("owngit settings set ") : start+end])
		if _, err := captureStdout(func() error { return settingsCommand(append(append([]string{"set"}, command...), remote...)) }); err != nil {
			t.Fatalf("settings set %v: %v", command, err)
		}
		if saved, err := fixture.store.LoginLimits(ctx); err != nil || saved != state.DefaultLoginLimits {
			t.Fatalf("after %v: saved=%+v err=%v", command, saved, err)
		}
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
		return repoCommand(append([]string{"settings", "set", "--kept-history", "off", "--protect-default-branch", "on", "--extra-ref-prefixes", "refs/notes/, refs/meta/"}, remote...))
	})
	if err != nil {
		t.Fatalf("repo settings set: %v", err)
	}
	var answer struct {
		Settings map[string]any `json:"settings"`
		Warnings []string       `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(printed), &answer); err != nil || len(answer.Warnings) != 2 ||
		!reflect.DeepEqual(answer.Settings["extra_ref_prefixes"], []any{"refs/notes/", "refs/meta/"}) {
		t.Fatalf("repo settings set printed %q (%v)", printed, err)
	}
	if saved, err := fixture.store.RepositoryRefPolicy(context.Background(), "project"); err != nil || !reflect.DeepEqual(saved, state.RepositoryRefPolicy{KeptHistory: state.KeptHistoryOff, ProtectDefaultBranch: true}) {
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
