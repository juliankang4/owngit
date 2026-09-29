//go:build !windows

package recovery

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The repository a bundle is made in is owner-only even when the process
// lets everyone read and write what it creates.
func TestCaptureRepositoryIsOwnerOnlyUnderAnyUmask(t *testing.T) {
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	previous := syscall.Umask(0)
	defer syscall.Umask(previous)
	runner := newPausingRunner(manager.Git, "project")
	done := make(chan error, 1)
	go func() {
		_, err := create(context.Background(), store, manager, runner, filepath.Join(root, "backup"), manifestLimit)
		done <- err
	}()
	select {
	case <-runner.reached:
	case err := <-done:
		t.Fatalf("backup ended before its bundle: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".backup.owngit-backup-*", "capture", "project.git"))
	noErr(t, err)
	var mode os.FileMode
	if len(matches) == 1 {
		info, statErr := os.Lstat(matches[0])
		noErr(t, statErr)
		mode = info.Mode()
	}
	close(runner.release)
	noErr(t, <-done)
	if len(matches) != 1 || !mode.IsDir() || mode.Perm() != 0o700 {
		t.Fatalf("capture repositories=%v mode=%v", matches, mode)
	}
}
