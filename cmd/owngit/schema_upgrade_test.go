package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/pullrequest"
	"owngit/internal/testfixture"
)

const baselineUpgradeLine = "state database upgraded from the committed baseline (no schema version) to schema 15"

// createBaselineStateForTest writes a committed baseline state whose
// repository exists with the pull request revision it records, so the
// backup before the upgrade can be made. It returns the state directory.
func createBaselineStateForTest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	repositories := filepath.Join(root, "repositories")
	if err := os.Mkdir(repositories, 0o700); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(repositories, testfixture.BaselineRepositoryID+".git")
	runPRGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	work := filepath.Join(root, "work")
	runPRGit(t, root, "init", "--initial-branch=main", work)
	runPRGit(t, work, "config", "user.name", "Baseline Test")
	runPRGit(t, work, "config", "user.email", "baseline@example.invalid")
	runPRGit(t, work, "commit", "--allow-empty", "-m", "target")
	target := prGitOutput(t, work, "rev-parse", "HEAD")
	runPRGit(t, work, "commit", "--allow-empty", "-m", "source")
	source := prGitOutput(t, work, "rev-parse", "HEAD")
	runPRGit(t, work, "push", remote, target+":refs/heads/main", source+":refs/heads/feature")
	sourceRef, targetRef := pullrequest.RevisionRefNames(testfixture.BaselinePullRequestNumber, source, target)
	runPRGit(t, remote, "update-ref", sourceRef, source)
	runPRGit(t, remote, "update-ref", targetRef, target)
	adminHash, err := auth.HashPassword("synthetic-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := testfixture.CreateCommittedBaselineState(context.Background(), stateDir, testfixture.BaselineStateOptions{
		RepositoryRoot: repositories, AdminPasswordHash: adminHash, SourceOID: source, TargetOID: target,
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
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "backed up the state to ") || !strings.HasPrefix(lines[1], "to go back to the earlier OwnGit") || lines[2] != baselineUpgradeLine {
		t.Fatalf("reported lines %q, want the backup, how to go back and %q, once", lines, baselineUpgradeLine)
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
	if got := run(); !strings.HasPrefix(got, "backed up the state to ") || !strings.HasSuffix(got, "\n"+baselineUpgradeLine+"\n") {
		t.Fatalf("first run wrote %q to stderr", got)
	}
	if got := run(); strings.TrimSpace(got) != "" {
		t.Fatalf("second run wrote %q to stderr", got)
	}
}
