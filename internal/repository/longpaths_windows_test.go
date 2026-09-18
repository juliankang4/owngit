//go:build windows

package repository

import (
	"context"
	"strings"
	"testing"
)

func TestWindowsOwnedRepositoryPersistsLongPathSupport(t *testing.T) {
	ctx := context.Background()
	manager, repositoryPath, _ := newTestRepository(t)
	assertLocalLongPaths := func() {
		t.Helper()
		result, err := manager.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "config", "--local", "--get", "core.longpaths")
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(result.Stdout)) != "true" {
			t.Fatalf("repository core.longpaths=%q, want true", result.Stdout)
		}
	}
	assertLocalLongPaths()
	if _, err := manager.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "config", "--local", "--unset-all", "core.longpaths"); err != nil {
		t.Fatal(err)
	}
	if err := manager.PrepareExisting(ctx); err != nil {
		t.Fatal(err)
	}
	assertLocalLongPaths()
}
