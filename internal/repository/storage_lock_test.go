package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/gitexec"
)

// A second manager stands for a server started from a copy of the state
// directory: same repository folder, its own lock.
func secondServer(first *Manager) *Manager {
	return &Manager{Store: first.Store, Git: first.Git, Locks: gitexec.NewLocks(), Root: first.RepositoryRoot()}
}

func TestClaimStorageRefusesASecondServerUntilTheFirstReleases(t *testing.T) {
	first, remote, _ := newTestRepository(t)
	second := secondServer(first)
	noErr(t, first.ClaimStorage())
	if err := second.ClaimStorage(); !errors.Is(err, ErrStorageInUse) {
		t.Fatalf("second claim error=%v, want ErrStorageInUse", err)
	}
	// Even a server that went on would not rewrite the hooks or create a
	// repository in the folder.
	hook, err := os.ReadFile(filepath.Join(remote, "hooks", "update"))
	noErr(t, err)
	if err := second.prepareRepository(context.Background(), "sample", second.Git); !errors.Is(err, ErrStorageInUse) {
		t.Fatalf("preparation by the second server error=%v", err)
	}
	if _, err := second.Create(context.Background(), "other", ""); !errors.Is(err, ErrStorageInUse) {
		t.Fatalf("creation by the second server error=%v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(remote, "hooks", "update")); string(after) != string(hook) {
		t.Fatal("the second server rewrote the hook")
	}
	first.ReleaseStorage()
	noErr(t, second.ClaimStorage(), "claim after release")
	second.ReleaseStorage()
}

// An empty folder, such as the mount point of a share that is not mounted,
// is not written to; the first repository created in it claims it.
func TestClaimStorageWaitsForTheFirstWriteToAnEmptyFolder(t *testing.T) {
	first, _, _ := newTestRepository(t)
	empty := filepath.Join(t.TempDir(), "mount-point")
	noErr(t, os.Mkdir(empty, 0o700))
	first.SetRoot(empty)
	noErr(t, first.ClaimStorage())
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Fatalf("claim wrote to an empty folder: %v %v", entries, err)
	}
	_, err := first.Create(context.Background(), "created", "")
	noErr(t, err)
	if err := secondServer(first).ClaimStorage(); !errors.Is(err, ErrStorageInUse) {
		t.Fatalf("second claim after the first repository error=%v", err)
	}
	first.ReleaseStorage()
}
