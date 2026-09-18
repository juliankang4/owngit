//go:build windows

package gitexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsRunnerSupportsLongProtectedRefPaths(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ref := "refs/owngit/pull-requests/1/merges/ba949734339254338f02ad60106ca106/result"
	minimumRepositoryLength := 261 - len(filepath.FromSlash(ref)) - len(".lock") - 1
	padding := minimumRepositoryLength - len(root) - len("project.git") - 1
	if padding < 32 {
		padding = 32
	}
	repositoryPath := filepath.Join(root, strings.Repeat("r", padding), "project.git")
	lockPath := filepath.Join(repositoryPath, filepath.FromSlash(ref)+".lock")
	if len(lockPath) <= 260 {
		t.Fatalf("long-path fixture is only %d characters: %s", len(lockPath), lockPath)
	}
	if err := os.MkdirAll(filepath.Dir(repositoryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := New("", filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, "", nil, "init", "--bare", "--initial-branch=main", repositoryPath); err != nil {
		t.Fatal(err)
	}
	treeResult, err := runner.Run(ctx, "", strings.NewReader(""), "--git-dir", repositoryPath, "mktree")
	if err != nil {
		t.Fatal(err)
	}
	treeOID := strings.TrimSpace(string(treeResult.Stdout))
	commitResult, err := runner.RunWithEnvironment(ctx, "", strings.NewReader("long-path probe\n"), []string{
		"GIT_AUTHOR_NAME=OwnGit",
		"GIT_AUTHOR_EMAIL=owngit@localhost",
		"GIT_COMMITTER_NAME=OwnGit",
		"GIT_COMMITTER_EMAIL=owngit@localhost",
	}, "--git-dir", repositoryPath, "commit-tree", treeOID)
	if err != nil {
		t.Fatal(err)
	}
	commitOID := strings.TrimSpace(string(commitResult.Stdout))
	if _, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "update-ref", ref, commitOID); err != nil {
		t.Fatalf("write %d-character protected ref lock path: %v", len(lockPath), err)
	}
	readback, err := runner.Run(ctx, "", nil, "--git-dir", repositoryPath, "rev-parse", "--verify", ref)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(readback.Stdout)) != commitOID {
		t.Fatalf("protected ref readback=%q, want %s", readback.Stdout, commitOID)
	}
	effective, err := runner.Run(ctx, "", nil, "config", "--get", "core.longpaths")
	if err != nil || strings.TrimSpace(string(effective.Stdout)) != "true" {
		t.Fatalf("effective core.longpaths=%q err=%v", effective.Stdout, err)
	}
	globalConfig, err := os.ReadFile(runner.GlobalConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(globalConfig)) != "" {
		t.Fatalf("runner wrote its isolated global config: %q", globalConfig)
	}
}
