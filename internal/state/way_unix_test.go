//go:build !windows

package state

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The walk resolves links itself, relative and absolute, and ".." in a
// link leaves the folder that the link before it led to, as the system
// would.
func TestWalkResolvesLinksLikeTheSystem(t *testing.T) {
	root := resolveTestPath(t, t.TempDir())
	noErr(t, os.MkdirAll(filepath.Join(root, "deep", "a", "b"), 0o700))
	noErr(t, os.Mkdir(filepath.Join(root, "deep", "other"), 0o700))
	noErr(t, os.Mkdir(filepath.Join(root, "real"), 0o700))
	noErr(t, os.Symlink(filepath.Join(root, "deep", "a", "b"), filepath.Join(root, "real", "inner")))
	noErr(t, os.Symlink("real/inner/../../other", filepath.Join(root, "relative")))
	noErr(t, os.Symlink(filepath.Join(root, "relative"), filepath.Join(root, "absolute")))
	path := filepath.Join(root, "absolute")
	want := filepath.Join(root, "deep", "other")
	dir, err := OpenDirectory(path, false)
	noErr(t, err)
	defer dir.Close()
	held, err := dir.Stat()
	noErr(t, err)
	named, err := os.Stat(path)
	noErr(t, err)
	if dir.Name() != want || !os.SameFile(held, named) {
		t.Fatalf("OpenDirectory reached %s, want %s", dir.Name(), want)
	}
	noErr(t, os.Symlink("missing", filepath.Join(root, "dangling")))
	if dir, err := OpenDirectory(filepath.Join(root, "dangling", "state"), true); err == nil {
		dir.Close()
		t.Fatal("a link to a missing folder was created through")
	}
	if _, err := os.Lstat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatalf("the missing folder behind a link was created: %v", err)
	}
}

// A lock file is made private only once it is known to be a file of this
// account with no other name: a link or a second name put where the lock
// goes leaves the file it leads to as it was.
func TestLockChangesNoFileAtItsName(t *testing.T) {
	for _, plant := range []struct {
		name  string
		place func(target, lock string) error
	}{
		{"link", os.Symlink},
		{"second name", os.Link},
	} {
		t.Run(plant.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(t.TempDir(), "program")
			noErr(t, os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755))
			noErr(t, os.Chmod(target, 0o755))
			lock := filepath.Join(dir, "operation.lock")
			noErr(t, plant.place(target, lock))
			if release, err := AcquireExclusiveFileLock(lock); err == nil {
				release()
				t.Fatal("the planted lock name was locked")
			}
			if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("the planted file changed: %v %v", info.Mode(), err)
			}
		})
	}
	dir := t.TempDir()
	noErr(t, syscall.Mkfifo(filepath.Join(dir, "operation.lock"), 0o600))
	if release, err := AcquireExclusiveFileLock(filepath.Join(dir, "operation.lock")); err == nil {
		release()
		t.Fatal("a named pipe was locked")
	}
}

// A link that a share holds leads the way through the share, even when it
// leads to a local folder: the share's server can change the link. The
// share is marked by replacing the filesystem check.
func TestFolderWayThroughALinkOnAShareIsShared(t *testing.T) {
	root := resolveTestPath(t, t.TempDir())
	share, local := filepath.Join(root, "share"), filepath.Join(root, "local")
	noErr(t, os.Mkdir(share, 0o700))
	noErr(t, os.Mkdir(local, 0o700))
	noErr(t, os.Symlink(local, filepath.Join(share, "repos")))
	shareInfo, err := os.Stat(share)
	noErr(t, err)
	previous := filesystemEnforced
	t.Cleanup(func() { filesystemEnforced = previous })
	filesystemEnforced = func(dir *os.File) (bool, error) {
		info, err := dir.Stat()
		if err != nil {
			return false, err
		}
		return !os.SameFile(info, shareInfo), nil
	}
	for _, path := range []string{filepath.Join(share, "repos", "new"), filepath.Join(share, "repos")} {
		if way := InspectFolderWay(path); !way.Shared || way.OnlyRoot {
			t.Errorf("%s: %+v, want the way through the share", path, way)
		}
	}
	if way := InspectFolderWay(filepath.Join(local, "new")); way.Shared {
		t.Errorf("the local folder counts as shared: %+v", way)
	}
}
