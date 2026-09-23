//go:build windows

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestWindowsLongRepositoryRootSupportsBrowserPullRequestDiff(t *testing.T) {
	ctx := context.Background()
	repositoryRoot := windowsLongServerRepositoryRoot(t, "project")
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runner, err := gitexec.New("", filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := manager.Create(ctx, "project", "long browser repository")
	if err != nil {
		t.Fatal(err)
	}
	repositoryPath, err := manager.Path(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(repositoryPath); got != 229 {
		t.Fatalf("bare repository path length=%d, want 229: %s", got, repositoryPath)
	}

	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Long Browser Test")
	apiRunGit(t, work, "config", "user.email", "long-browser@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "base")
	targetOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	apiRunGit(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "feature")
	sourceOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	if _, err := runner.Run(ctx, repositoryPath, nil,
		"--git-dir", ".", "fetch", "--no-tags", "--no-write-fetch-head", work,
		"refs/heads/main:refs/heads/main", "refs/heads/feature:refs/heads/feature"); err != nil {
		t.Fatal(err)
	}

	app := &App{Repositories: manager}
	files, truncated, err := app.comparePullRequestRevisions(ctx, stored.ID, sourceOID, targetOID)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(files) != 1 || files[0].Path != "feature.txt" || files[0].Status != "added" || len(files[0].Hunks) == 0 {
		t.Fatalf("long-root pull request diff files=%+v truncated=%v", files, truncated)
	}
}

func windowsLongServerRepositoryRoot(t *testing.T, repositoryID string) string {
	t.Helper()
	base := t.TempDir()
	const targetLength = 229
	bareName := repositoryID + ".git"
	padding := targetLength - len(filepath.Join(base, bareName)) - 1
	if padding < 1 || padding > 240 {
		t.Fatalf("temporary path cannot form a %d-byte bare repository path: %s", targetLength, base)
	}
	root := filepath.Join(base, strings.Repeat("s", padding))
	if got := len(filepath.Join(root, bareName)); got != targetLength {
		t.Fatalf("constructed bare repository path length=%d, want %d", got, targetLength)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
