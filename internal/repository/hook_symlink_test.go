package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetentionHookRefusesHooksDirectorySymlink(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	repositoryPath, err := manager.Path("sample")
	noErr(t, err)
	hooks := filepath.Join(repositoryPath, "hooks")
	noErr(t, os.Rename(hooks, hooks+".original"))
	external := t.TempDir()
	if err := os.Symlink(external, hooks); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}

	err = manager.PrepareExisting(context.Background())
	if err == nil || !strings.Contains(err.Error(), `prepare repository "sample"`) {
		t.Fatalf("hook refresh error=%v, want the repository name", err)
	}
	if _, statErr := os.Lstat(filepath.Join(external, "update")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hook refresh wrote outside the repository: %v", statErr)
	}
}

func TestRetentionHookRefusesUpdateSymlink(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	created, err := manager.Create(context.Background(), "linked-hook", "")
	noErr(t, err)
	repositoryPath, err := manager.Path(created.ID)
	noErr(t, err)
	update := filepath.Join(repositoryPath, "hooks", "update")
	noErr(t, os.Rename(update, update+".original"))
	external := filepath.Join(t.TempDir(), "outside")
	const sentinel = "outside stays unchanged\n"
	noErr(t, os.WriteFile(external, []byte(sentinel), 0o600))
	if err := os.Symlink(external, update); err != nil {
		t.Skipf("file symlinks are unavailable: %v", err)
	}

	err = manager.PrepareExisting(context.Background())
	if err == nil || !strings.Contains(err.Error(), `prepare repository "linked-hook"`) {
		t.Fatalf("hook refresh error=%v, want the repository name", err)
	}
	content, readErr := os.ReadFile(external)
	noErr(t, readErr)
	if string(content) != sentinel {
		t.Fatalf("hook refresh changed the outside file: %q", content)
	}
}
