//go:build windows

package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCreationRollbackPreservesMovedRootJunction(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	path := filepath.Join(manager.RepositoryRoot(), "draft")
	noErr(t, os.Mkdir(path, 0o700))
	noErr(t, manager.InitBareRepository(context.Background(), path, CreateOptions{}))
	creation, err := captureEmptyCreation(path)
	noErr(t, err)
	t.Cleanup(func() { _ = creation.parent.Close() })
	moved := path + "-moved"
	noErr(t, os.Rename(path, moved))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "cmd.exe", "/c", "mklink", "/J", path, moved).CombinedOutput()
	if err != nil {
		t.Fatalf("create synthetic junction: %v %s", err, output)
	}
	if err := creation.rollback(path); err == nil {
		t.Fatal("rollback accepted a junction to the moved original")
	}
	if _, err := os.Stat(filepath.Join(moved, "config")); err != nil {
		t.Fatalf("rollback changed the moved tree: %v", err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rollback changed the substituted junction: %v %v", info, err)
	}
}
