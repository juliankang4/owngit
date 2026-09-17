package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/state"
)

func TestUsageNamesTheOwngitCommand(t *testing.T) {
	var output bytes.Buffer
	printUsage(&output)
	if !strings.Contains(output.String(), "Usage: owngit") {
		t.Fatalf("usage does not name the owngit command: %q", output.String())
	}
}

func TestDefaultStatePathUsesOwngitNames(t *testing.T) {
	configRoot := filepath.Join("platform", "config")
	home := filepath.Join("users", "owner")
	if got, want := defaultStatePath(configRoot, home), filepath.Join(configRoot, "owngit"); got != want {
		t.Fatalf("configured state path = %q, want %q", got, want)
	}
	if got, want := defaultStatePath("", home), filepath.Join(home, ".owngit"); got != want {
		t.Fatalf("fallback state path = %q, want %q", got, want)
	}
}

func TestGlobalHelpFlagsPrintUsageAndSucceed(t *testing.T) {
	// Route through run(), not printUsage directly: the real failure was that
	// -h/--help fell through into the serve flag set and returned an error.
	for _, arg := range []string{"help", "-h", "--help"} {
		output, err := captureStdout(func() error { return run([]string{arg}) })
		if err != nil {
			t.Errorf("run(%q) returned an error: %v", arg, err)
		}
		if !strings.Contains(output, "Usage: owngit") {
			t.Errorf("run(%q) did not print usage: %q", arg, output)
		}
	}
}

func captureStdout(handle func() error) (string, error) {
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = writer
	runErr := handle()
	os.Stdout = original
	writer.Close()
	content, _ := io.ReadAll(reader)
	reader.Close()
	return string(content), runErr
}

func TestResetAdminPreservesRepositoryDataAndRevokesSession(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	preserved := filepath.Join(repositoryRoot, "project.git", "objects", "sentinel")
	if err := os.MkdirAll(filepath.Dir(preserved), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preserved, []byte("repository data"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	accessHash, _ := auth.HashPassword("shared-password")
	adminHash, _ := auth.HashPassword("old-admin-password")
	if err := store.CompleteSetup(context.Background(), repositoryRoot, "password", accessHash, adminHash, true); err != nil {
		t.Fatal(err)
	}
	settings, _ := store.Settings(context.Background())
	if err := store.CreateSession(context.Background(), "admin-session", "admin", "csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	passwordFile := filepath.Join(root, "new-password")
	if err := os.WriteFile(passwordFile, []byte("new-admin-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := resetAdmin([]string{"--state-dir", stateDir, "--password-file", passwordFile}); err != nil {
		t.Fatal(err)
	}
	store, err = state.Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	encoded, _ := store.PasswordHash(context.Background(), "admin")
	if !auth.CheckPassword(encoded, "new-admin-password") {
		t.Fatal("new administrator password did not verify")
	}
	if _, ok, err := store.Session(context.Background(), "admin-session", "admin", time.Now()); err != nil || ok {
		t.Fatalf("administrator session survived reset: ok=%v err=%v", ok, err)
	}
	content, err := os.ReadFile(preserved)
	if err != nil || string(content) != "repository data" {
		t.Fatalf("repository data changed: content=%q err=%v", content, err)
	}
}

func TestReadPrivatePasswordRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode check does not apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("valid-password"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivatePassword(path); err == nil {
		t.Fatal("group-readable password file was accepted")
	}
}

func TestOwnerOriginRejectsCredentialAndPath(t *testing.T) {
	for _, value := range []string{"http://user@example.test", "http://example.test/path", "ftp://example.test"} {
		if _, err := ownerOrigin(value, "127.0.0.1:7654"); err == nil {
			t.Errorf("owner origin %q was accepted", value)
		}
	}
	if got, err := ownerOrigin("", "0.0.0.0:7654"); err != nil || got != "http://127.0.0.1:7654" {
		t.Fatalf("default owner origin=%q err=%v", got, err)
	}
}
