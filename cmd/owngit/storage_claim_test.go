package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// A server started from a copy of a state directory must not take over the
// repository folder of a running server: it would rewrite every retention
// hook to its own runtime paths, and pushes to the original would fail once
// the copy is gone. With the original stopped, the state may be moved.
func TestServeRefusesACopiedStateThatSharesARunningServersRepositories(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	original := filepath.Join(root, "state")
	copied := filepath.Join(root, "state-copy")
	repositoriesRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoriesRoot, 0o700))
	store, err := state.Open(ctx, original)
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoriesRoot, "open", "", "synthetic-admin-hash", true))
	runner, err := gitexec.New("", filepath.Join(original, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	_, err = manager.Create(ctx, "shared", "")
	noErr(t, err)
	noErr(t, store.Close())
	copyTree(t, original, copied)
	hookPath := filepath.Join(repositoriesRoot, "shared.git", "hooks", "update")
	readHook := func() string {
		t.Helper()
		content, err := os.ReadFile(hookPath)
		noErr(t, err)
		return string(content)
	}

	instance := startServed(t, original)
	hook := readHook()
	if !strings.Contains(hook, filepath.Join(original, "runtime")) {
		t.Fatalf("hook does not name the original runtime:\n%s", hook)
	}
	copyContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var copyLog strings.Builder
	err = serveWithContext(copyContext, []string{"--state-dir", copied, "--listen", "127.0.0.1:0", "--no-open"},
		func(string) error { return nil }, func(format string, arguments ...any) { copyLog.WriteString(format + "\n") })
	if !errors.Is(err, repository.ErrStorageInUse) {
		instance.stop()
		t.Fatalf("copied state serve error=%v, want ErrStorageInUse\n%s", err, copyLog.String())
	}
	if !strings.Contains(err.Error(), repositoriesRoot) && !strings.Contains(err.Error(), filepath.Base(repositoriesRoot)) {
		t.Errorf("error does not name the repository folder: %v", err)
	}
	if got := readHook(); got != hook {
		instance.stop()
		t.Fatalf("the refused copy rewrote the hook:\n%s", got)
	}
	instance.stop()

	// Moving a stopped state directory keeps working: the copy now serves.
	moved := startServed(t, copied)
	moved.stop()
	if got := readHook(); !strings.Contains(got, filepath.Join(copied, "runtime")) {
		t.Fatalf("the moved state did not refresh the hook:\n%s", got)
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	noErr(t, filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		defer source.Close()
		destination, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(destination, source); err != nil {
			destination.Close()
			return err
		}
		return destination.Close()
	}))
}
