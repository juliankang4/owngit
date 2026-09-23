//go:build windows

package pullrequest

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

func TestWindowsLongRepositoryRootSupportsPullRequestMerge(t *testing.T) {
	ctx := context.Background()
	repositoryRoot := windowsLongPullRequestRepositoryRoot(t, "project")
	stateRoot := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(ctx, stateRoot)
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	noErr(t, store.CompleteSetup(ctx, repositoryRoot, "open", "", "synthetic-admin-hash", true))
	runner, err := gitexec.New("", filepath.Join(stateRoot, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := manager.Create(ctx, "project", "long pull request repository")
	noErr(t, err)
	repositoryPath, err := manager.Path(stored.ID)
	noErr(t, err)
	if got := len(repositoryPath); got != 229 {
		t.Fatalf("bare repository path length=%d, want 229: %s", got, repositoryPath)
	}

	work := filepath.Join(t.TempDir(), "work")
	runFixtureGit(t, "", "init", "--initial-branch=main", work)
	runFixtureGit(t, work, "config", "user.name", "Long PR Test")
	runFixtureGit(t, work, "config", "user.email", "long-pr@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o600))
	runFixtureGit(t, work, "add", ".")
	runFixtureGit(t, work, "commit", "-m", "base")
	runFixtureGit(t, work, "checkout", "-b", "feature")
	noErr(t, os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o600))
	runFixtureGit(t, work, "add", ".")
	runFixtureGit(t, work, "commit", "-m", "feature")
	sourceOID := fixtureGitResultOutput(t, work, "rev-parse", "HEAD").Stdout
	runFixtureGit(t, work, "checkout", "main")
	noErr(t, os.WriteFile(filepath.Join(work, "target.txt"), []byte("target\n"), 0o600))
	runFixtureGit(t, work, "add", ".")
	runFixtureGit(t, work, "commit", "-m", "target")
	targetOID := fixtureGitResultOutput(t, work, "rev-parse", "HEAD").Stdout
	if _, err := runner.Run(ctx, repositoryPath, nil,
		"--git-dir", ".", "fetch", "--no-tags", "--no-write-fetch-head", work,
		"refs/heads/main:refs/heads/main", "refs/heads/feature:refs/heads/feature"); err != nil {
		t.Fatal(err)
	}

	service := &Service{Store: store, Repositories: manager}
	created, err := service.Create(ctx, CreateInput{
		Repository: stored.ID, Title: "Long root merge", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
	})
	noErr(t, err)
	merged, err := service.Merge(ctx, stored.ID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
	noErr(t, err)
	if merged.Merge == nil || merged.Merge.Mode != "merge_commit" || merged.Merge.OID == "" {
		t.Fatalf("long-root merge=%+v", merged.Merge)
	}
	result, err := runner.Run(ctx, repositoryPath, nil, "--git-dir", ".", "rev-parse", "--verify", "refs/heads/main")
	noErr(t, err)
	if strings.TrimSpace(string(result.Stdout)) != merged.Merge.OID {
		t.Fatalf("long-root target=%q, want %s", result.Stdout, merged.Merge.OID)
	}
}

func windowsLongPullRequestRepositoryRoot(t *testing.T, repositoryID string) string {
	t.Helper()
	base := t.TempDir()
	const targetLength = 229
	bareName := repositoryID + ".git"
	padding := targetLength - len(filepath.Join(base, bareName)) - 1
	if padding < 1 || padding > 240 {
		t.Fatalf("temporary path cannot form a %d-byte bare repository path: %s", targetLength, base)
	}
	root := filepath.Join(base, strings.Repeat("p", padding))
	if got := len(filepath.Join(root, bareName)); got != targetLength {
		t.Fatalf("constructed bare repository path length=%d, want %d", got, targetLength)
	}
	noErr(t, os.MkdirAll(root, 0o700))
	return root
}
