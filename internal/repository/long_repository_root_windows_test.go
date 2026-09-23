//go:build windows

package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/state"
)

func TestWindowsLongRepositoryRootSupportsCreateBrowsePinnedAndRestore(t *testing.T) {
	ctx := context.Background()
	repositoryRoot := windowsLongRepositoryRoot(t, "project")
	store, err := state.Open(ctx, filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runner, err := gitexec.New("", filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := manager.Create(ctx, "project", "long repository root")
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
	if err := manager.PrepareExisting(ctx); err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(t.TempDir(), "work")
	runGit(t, "", "init", "--initial-branch=main", work)
	runGit(t, work, "config", "user.name", "Long Root Test")
	runGit(t, work, "config", "user.email", "long-root@example.invalid")
	commitFile(t, work, "base\n", "base", "2024-01-01T00:00:00Z")
	baseOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "checkout", "-b", "feature")
	commitFile(t, work, "head\n", "head", "2024-01-02T00:00:00Z")
	headOID := gitOutput(t, work, "rev-parse", "HEAD")
	if _, err := runner.Run(ctx, repositoryPath, nil,
		"--git-dir", ".", "fetch", "--no-tags", "--no-write-fetch-head", work,
		"refs/heads/main:refs/heads/main", "refs/heads/feature:refs/heads/feature"); err != nil {
		t.Fatal(err)
	}

	summary, err := manager.Summary(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.DefaultBranch != "main" || summary.DefaultOID != baseOID || len(summary.Branches) != 2 {
		t.Fatalf("long-root summary=%+v", summary)
	}
	commitOID, entries, err := manager.Tree(ctx, stored.ID, "refs/heads/feature", "")
	if err != nil {
		t.Fatal(err)
	}
	if commitOID != headOID || len(entries) != 1 || entries[0].Path != "file.txt" {
		t.Fatalf("long-root tree commit=%s entries=%+v", commitOID, entries)
	}
	_, blob, err := manager.ReadBlob(ctx, stored.ID, "refs/heads/feature", "file.txt", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(blob.Content) != "head\n" || blob.Truncated {
		t.Fatalf("long-root blob=%q truncated=%v", blob.Content, blob.Truncated)
	}
	activity, err := manager.Activity(ctx, stored.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if activity.Commits != 2 || activity.Incomplete {
		t.Fatalf("long-root activity=%+v", activity)
	}

	pinned, err := manager.PinRepository(ctx, stored.ID, baseOID, headOID)
	if err != nil {
		t.Fatal(err)
	}
	change, err := pinned.ReadChange(ctx, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	if change.BaseOID != baseOID || change.HeadOID != headOID || len(change.Patch) == 0 || change.Truncated {
		t.Fatalf("long-root pinned change=%+v", change)
	}
	pinnedBlob, err := pinned.ReadBlob(ctx, PinnedHead, "file.txt", 0, 4096, 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if string(pinnedBlob.Content) != "head\n" || pinnedBlob.HasMore {
		t.Fatalf("long-root pinned blob=%+v", pinnedBlob)
	}

	request := RestoreRequest{Source: headOID, Target: "main", Mode: RestoreFiles, Paths: []string{"file.txt"}}
	preview, err := manager.PreviewRestore(ctx, stored.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanApply || preview.ExpectedHead != baseOID || len(preview.Changes) != 1 {
		t.Fatalf("long-root restore preview=%+v", preview)
	}
	request.ExpectedHead = preview.ExpectedHead
	restored, err := manager.ApplyRestore(ctx, stored.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Created || restored.CommitOID == "" || restored.CommitOID == headOID {
		t.Fatalf("long-root restore result=%+v", restored)
	}
}

func windowsLongRepositoryRoot(t *testing.T, repositoryID string) string {
	t.Helper()
	base := t.TempDir()
	const targetLength = 229
	bareName := repositoryID + ".git"
	padding := targetLength - len(filepath.Join(base, bareName)) - 1
	if padding < 1 || padding > 240 {
		t.Fatalf("temporary path cannot form a %d-byte bare repository path: %s", targetLength, base)
	}
	root := filepath.Join(base, strings.Repeat("r", padding))
	if got := len(filepath.Join(root, bareName)); got != targetLength {
		t.Fatalf("constructed bare repository path length=%d, want %d", got, targetLength)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
