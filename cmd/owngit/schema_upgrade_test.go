package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/testfixture"
)

const baselineUpgradeLine = "state database upgraded from the committed baseline (no schema version) to schema 15"

func createBaselineStateForTest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := testfixture.CreateCommittedBaselineState(context.Background(), stateDir, testfixture.BaselineStateOptions{
		RepositoryRoot: filepath.Join(root, "repositories"), AdminPasswordHash: "synthetic-admin-hash",
	}); err != nil {
		t.Fatal(err)
	}
	return stateDir
}

// serve passes its log to openState, so the upgrade appears there once.
func TestOpenStateReportsTheSchemaUpgradeOnce(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	var lines []string
	report := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	for range 2 {
		store, err := openState(context.Background(), stateDir, report)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if len(lines) != 1 || lines[0] != baselineUpgradeLine {
		t.Fatalf("reported lines %q, want one %q", lines, baselineUpgradeLine)
	}

	var fresh []string
	store, err := openState(context.Background(), filepath.Join(t.TempDir(), "state"), func(format string, args ...any) {
		fresh = append(fresh, fmt.Sprintf(format, args...))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if len(fresh) != 0 {
		t.Fatalf("a new database reported %q", fresh)
	}
}

// An offline command writes the upgrade to standard error.
func TestOfflineCommandWritesTheSchemaUpgradeToStderr(t *testing.T) {
	stateDir := createBaselineStateForTest(t)
	run := func() string {
		t.Helper()
		stderrPath := filepath.Join(t.TempDir(), "stderr")
		stderrFile, err := os.Create(stderrPath)
		if err != nil {
			t.Fatal(err)
		}
		stdoutFile, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
		if err != nil {
			t.Fatal(err)
		}
		stderr, stdout := os.Stderr, os.Stdout
		os.Stderr, os.Stdout = stderrFile, stdoutFile
		runErr := runCommand("approve-host", []string{"--state-dir", stateDir, "owngit.example.test"})
		os.Stderr, os.Stdout = stderr, stdout
		stderrFile.Close()
		stdoutFile.Close()
		if runErr != nil {
			t.Fatal(runErr)
		}
		data, err := os.ReadFile(stderrPath)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if got := run(); got != baselineUpgradeLine+"\n" {
		t.Fatalf("first run wrote %q to stderr", got)
	}
	if got := run(); strings.TrimSpace(got) != "" {
		t.Fatalf("second run wrote %q to stderr", got)
	}
}
