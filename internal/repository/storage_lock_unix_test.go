//go:build !windows

package repository

import (
	"os"
	"path/filepath"
	"testing"
)

// Whoever else can write to a shared repository folder may put a link, or a
// second name of a file, where the lock file goes. Claiming the folder must
// then change neither that file's mode nor anything else about it.
func TestStorageClaimChangesNoFileAtTheLockName(t *testing.T) {
	for _, plant := range []struct {
		name  string
		place func(target, lock string) error
	}{
		{"link", os.Symlink},
		{"second name", os.Link},
	} {
		t.Run(plant.name, func(t *testing.T) {
			manager, _, _ := newTestRepository(t)
			target := filepath.Join(t.TempDir(), "program")
			noErr(t, os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755))
			noErr(t, os.Chmod(target, 0o755))
			noErr(t, plant.place(target, filepath.Join(manager.RepositoryRoot(), storageLockName)))
			if err := manager.ClaimStorage(); err == nil {
				manager.ReleaseStorage()
				t.Fatal("the folder was claimed through the planted lock name")
			}
			if _, err := manager.Create(t.Context(), "refused", ""); err == nil {
				t.Fatal("creation proceeded without owning the storage claim")
			}
			if _, err := os.Lstat(filepath.Join(manager.RepositoryRoot(), "refused.git")); !os.IsNotExist(err) {
				t.Fatalf("unclaimed creation published storage: %v", err)
			}
			if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("the planted file changed: %v %v", info.Mode(), err)
			}
		})
	}
}
