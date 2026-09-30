package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/importsync"
	"owngit/internal/state"
)

func TestImportConfigureChangesOnlyTheGivenOptions(t *testing.T) {
	root := t.TempDir()
	passwordPath := filepath.Join(root, "admin")
	noErr(t, os.WriteFile(passwordPath, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(passwordPath, false))
	fixture := startImportCLIServer(t)
	remote := []string{"--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath}

	printed, err := captureStdout(func() error {
		return importCommand(append([]string{"configure", fixture.repositoryID, "--redirects", "approved", "--approved-origin", "https://Mirror.Example",
			"--limit", "run_seconds=3h", "--limit", "pack_bytes=32GiB", "--limit", "refs=100000"}, remote...))
	})
	if err != nil || !strings.Contains(printed, "https://mirror.example") || !strings.Contains(printed, "pack_bytes=34359738368, run_seconds=10800, refs=100000") {
		t.Fatalf("configure output=%q err=%v", printed, err)
	}
	printed, err = captureStdout(func() error {
		return importCommand(append([]string{"configure", fixture.repositoryID, "--allow-exceptional-destination", "--json"}, remote...))
	})
	var response struct {
		Options importsync.OptionsStatus `json:"options"`
	}
	if err != nil || json.Unmarshal([]byte(printed), &response) != nil || !response.Options.AllowReservedAddresses ||
		response.Options.Redirects != "approved" || response.Options.Limits.RunSeconds != 10800 {
		t.Fatalf("configure --json output=%q err=%v", printed, err)
	}
	source, _, err := fixture.store.ImportSource(context.Background(), fixture.repositoryID)
	if err != nil || source.URL != "https://example.invalid/team/project.git" || source.Options.Limits.Refs != 100000 {
		t.Fatalf("stored source = %+v, %v", source, err)
	}

	for _, arguments := range [][]string{
		{"configure", fixture.repositoryID},
		{"configure", fixture.repositoryID, "--limit", "refs"},
		{"configure", fixture.repositoryID, "--limit", "packets=1"},
		{"configure", fixture.repositoryID, "--limit", "run_seconds=1.5s"},
		{"configure", fixture.repositoryID, "--limit", "pack_bytes=9999999999TiB"},
		{"configure", fixture.repositoryID, "--limit", "refs=0"},
		{"configure", fixture.repositoryID, "--limit", "refs=1", "--limit", "refs=2"},
		// A malformed origin is refused even when the policy would not use it.
		{"configure", fixture.repositoryID, "--redirects", "refuse", "--approved-origin", "https://mirror.example/path"},
		{"configure", fixture.repositoryID, "--approved-origin", "mirror.example"},
	} {
		if _, err := captureStdout(func() error { return importCommand(append(arguments, remote...)) }); err == nil {
			t.Errorf("%v was accepted", arguments)
		}
	}

	status, err := captureStdout(func() error {
		return importCommand(append([]string{"status", fixture.repositoryID}, remote...))
	})
	if err != nil || !strings.Contains(status, "Exceptional destination: allowed") || !strings.Contains(status, "Changed limits: pack_bytes=34359738368") {
		t.Fatalf("status output=%q err=%v", status, err)
	}
	status, err = captureStdout(func() error {
		return importCommand(append([]string{"status", fixture.repositoryID, "--json"}, remote...))
	})
	if err != nil || !strings.Contains(status, `"allow_reserved_addresses": true`) || !strings.Contains(status, `"verify_seconds": 600`) {
		t.Fatalf("status --json output=%q err=%v", status, err)
	}
}

func TestImportAddSendsSourceOptions(t *testing.T) {
	root := t.TempDir()
	passwordPath := filepath.Join(root, "admin")
	noErr(t, os.WriteFile(passwordPath, []byte("admin-password\n"), 0o600))
	noErr(t, state.ProtectPrivatePath(passwordPath, false))
	fixture := startImportCLIServer(t)
	if _, err := captureStdout(func() error {
		return importCommand([]string{
			"add", "plain", "http://example.invalid/team/plain.git", "--allow-plain-http", "--limit", "lfs_objects=400000",
			"--server", fixture.url, "--accept-insecure-http", "--password-file", passwordPath,
		})
	}); err != nil {
		t.Fatal(err)
	}
	source, exists, err := fixture.store.ImportSource(context.Background(), "plain")
	if err != nil || !exists || !source.Options.AllowPlainHTTP || source.Options.Limits.LFSObjects != 400000 {
		t.Fatalf("added source = %+v, %v, %v", source, exists, err)
	}
}
