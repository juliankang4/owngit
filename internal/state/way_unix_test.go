//go:build !windows

package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	markShare(t, share)
	for _, path := range []string{filepath.Join(share, "repos", "new"), filepath.Join(share, "repos")} {
		if way := InspectFolderWay(path); !way.Shared || way.OnlyRoot {
			t.Errorf("%s: %+v, want the way through the share", path, way)
		}
	}
	if way := InspectFolderWay(filepath.Join(local, "new")); way.Shared {
		t.Errorf("the local folder counts as shared: %+v", way)
	}
}

// OpenDirectory, which opens the folders for OwnGit's state, log and serve
// error, does not trust owners and modes that a share shows: a link that a
// share holds is refused even when it leads to a local folder of this
// account. The share is marked by replacing the filesystem check.
func TestOpenDirectoryRefusesAWayThroughAShare(t *testing.T) {
	root := resolveTestPath(t, t.TempDir())
	share, local := filepath.Join(root, "share"), filepath.Join(root, "local")
	noErr(t, os.Mkdir(share, 0o700))
	noErr(t, os.Mkdir(local, 0o700))
	noErr(t, os.Symlink(local, filepath.Join(share, "state")))
	markShare(t, share)
	for _, create := range []bool{false, true} {
		for _, path := range []string{filepath.Join(share, "state"), filepath.Join(share, "state", "new")} {
			if dir, err := OpenDirectory(path, create); err == nil || !strings.Contains(err.Error(), "does not enforce") {
				if dir != nil {
					dir.Close()
				}
				t.Errorf("OpenDirectory(%s, %t) error=%v, want the share refused", path, create, err)
			}
		}
	}
	if _, err := os.Lstat(filepath.Join(local, "new")); !os.IsNotExist(err) {
		t.Fatalf("a folder was created through the share: %v", err)
	}
}

// markShare makes the filesystem check report the folder share as a share.
func markShare(t *testing.T, share string) {
	t.Helper()
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
}

// State is opened in the directory that was checked and is held: when its
// path names another directory by the time the state is inspected, the
// open reports a change instead of using that directory.
func TestStateIsOpenedOnlyInTheHeldDirectory(t *testing.T) {
	root := resolveTestPath(t, t.TempDir())
	path := filepath.Join(root, "state")
	held, err := CreateDirectory(path)
	noErr(t, err)
	defer held.Close()
	noErr(t, os.Rename(path, filepath.Join(root, "checked")))
	noErr(t, os.Mkdir(path, 0o700))
	store, err := OpenIn(context.Background(), held)
	if store != nil {
		store.Close()
	}
	if !errors.Is(err, ErrInspectionUnstable) {
		t.Fatalf("OpenIn error=%v, want the replaced directory reported as a change", err)
	}
	if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
		t.Fatalf("the directory now at the path was used: %v %v", entries, err)
	}
}
