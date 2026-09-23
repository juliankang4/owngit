package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// A deletion that stopped after its commit point stays recorded while its
// storage is not mounted, and the first serve with the storage back completes
// it before it accepts requests.
func TestServeCompletesUnfinishedRepositoryDeletion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	repositoriesRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoriesRoot, 0o700))
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositoriesRoot, "open", "", "synthetic-admin-hash", true))
	runner, err := gitexec.New("", filepath.Join(stateDir, "runtime"))
	noErr(t, err)
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	if _, err := manager.Create(ctx, "stopped", ""); err != nil {
		t.Fatal(err)
	}
	storageRoot, err := manager.CanonicalStorageRoot()
	noErr(t, err)
	moved := ".owngit-removed/stopped-20270115T080000Z.git"
	markerToken := strings.Repeat("c", 32)
	noErr(t, store.BeginRepositoryDeletion(ctx, state.RepositoryDeletion{
		RepositoryID: "stopped", Mode: state.RepositoryDeletionKeepFiles, Root: storageRoot, Moved: moved, Marker: markerToken, CreatedAt: time.Now(),
	}))
	noErr(t, os.MkdirAll(filepath.Join(storageRoot, ".owngit-removed"), 0o700))
	// Delete writes this marker before its commit point.
	noErr(t, os.WriteFile(filepath.Join(storageRoot, ".owngit-deletion-stopped"), []byte("token "+markerToken+"\n"), 0o600))
	noErr(t, store.Close())

	// The storage is not mounted: its mount point is an empty directory. The
	// deleted repository was the only one, so nothing else stops the start.
	mounted := storageRoot + ".mounted"
	noErr(t, os.Rename(storageRoot, mounted))
	noErr(t, os.Mkdir(storageRoot, 0o700))
	instance := startServed(t, stateDir)
	instance.stop()
	if !strings.Contains(instance.log(), "unfinished repository deletion was not completed") || !strings.Contains(instance.log(), "storage may be unavailable") {
		t.Fatalf("unmounted storage was not reported:\n%s", instance.log())
	}
	if entries, err := os.ReadDir(storageRoot); err != nil || len(entries) != 0 {
		t.Fatalf("serve wrote to the empty mount point: %v err=%v", entries, err)
	}
	unmountedState, err := state.Open(ctx, stateDir)
	noErr(t, err)
	if deletions, err := unmountedState.RepositoryDeletions(ctx); err != nil || len(deletions) != 1 {
		t.Fatalf("deletion intent was dropped %+v err=%v", deletions, err)
	}
	noErr(t, unmountedState.Close())

	noErr(t, os.Remove(storageRoot))
	noErr(t, os.Rename(mounted, storageRoot))
	instance = startServed(t, stateDir)
	instance.stop()

	if _, err := os.Stat(filepath.Join(storageRoot, filepath.FromSlash(moved), "HEAD")); err != nil {
		t.Fatalf("kept repository is missing: %v\n%s", err, instance.log())
	}
	for _, name := range []string{"stopped.git", ".owngit-deletion-stopped"} {
		if _, err := os.Lstat(filepath.Join(storageRoot, name)); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", name, err)
		}
	}
	reopened, err := state.Open(ctx, stateDir)
	noErr(t, err)
	defer reopened.Close()
	if deletions, err := reopened.RepositoryDeletions(ctx); err != nil || len(deletions) != 0 {
		t.Fatalf("deletion intent remains %+v err=%v", deletions, err)
	}
}
