package recovery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

// A deleted repository, a repository whose deletion stopped after its records
// were removed, and kept folders are all absent from a backup and its restore.
func TestBackupAndRestoreOmitDeletedRepositories(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	for _, name := range []string{"kept", "erased", "stopped"} {
		if _, err := manager.Create(ctx, name, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.Delete(ctx, "kept", repository.DeleteKeepFiles); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Delete(ctx, "erased", repository.DeleteFiles); err != nil {
		t.Fatal(err)
	}
	storageRoot, err := manager.CanonicalStorageRoot()
	noErr(t, err)
	// The process stopped right after the deletion commit point: the records
	// are gone and the directory is still at its repository path.
	noErr(t, store.BeginRepositoryDeletion(ctx, state.RepositoryDeletion{
		RepositoryID: "stopped", Mode: state.RepositoryDeletionKeepFiles, Root: storageRoot,
		Moved: ".owngit-removed/stopped-20270115T080000Z.git", Marker: strings.Repeat("c", 32), CreatedAt: time.Now(),
	}))
	if _, err := os.Stat(filepath.Join(storageRoot, "stopped.git")); err != nil {
		t.Fatalf("fixture directory missing: %v", err)
	}

	backup := filepath.Join(root, "backup")
	noErr(t, Create(ctx, store, manager, backup))
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	if len(manifest.Repositories) != 1 || manifest.Repositories[0].ID != "project" {
		t.Fatalf("backup repositories=%+v", manifest.Repositories)
	}
	entries, err := os.ReadDir(filepath.Join(backup, "repositories"))
	noErr(t, err)
	for _, entry := range entries {
		if entry.Name() != "project.bundle" {
			t.Fatalf("backup holds %s", entry.Name())
		}
	}

	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
	noErr(t, Restore(ctx, backup, restoredState, restoredRepositories, ""))
	restoredStore, err := state.Open(ctx, restoredState)
	noErr(t, err)
	defer restoredStore.Close()
	listed, err := restoredStore.Repositories(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != "project" {
		t.Fatalf("restored repositories=%+v err=%v", listed, err)
	}
	if deletions, err := restoredStore.RepositoryDeletions(ctx); err != nil || len(deletions) != 0 {
		t.Fatalf("restore carried deletion intents %+v err=%v", deletions, err)
	}
	names, err := os.ReadDir(restoredRepositories)
	noErr(t, err)
	for _, entry := range names {
		if entry.Name() != "project.git" && !strings.HasPrefix(entry.Name(), ".owngit-restore") {
			t.Fatalf("restored repository folder holds %s", entry.Name())
		}
	}
}
