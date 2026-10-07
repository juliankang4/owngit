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

// A directory other accounts can write is refused without changing its mode.
func TestStateDirectoryRefusesOtherWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	held, err := CreateDirectory(path)
	noErr(t, err)
	noErr(t, held.Close())
	defer func() { noErr(t, os.Chmod(path, 0o700)) }()
	for _, mode := range []os.FileMode{0o777, 0o777 | os.ModeSticky, 0o770, 0o500} {
		noErr(t, os.Chmod(path, mode))
		info, err := os.Stat(path)
		noErr(t, err)
		stat := info.Sys().(*syscall.Stat_t)
		unsafe := mode.Perm()&0o002 != 0 || mode.Perm()&0o020 != 0 && !OwnPrivateGroup(stat.Gid) && !rootEquivalentGroup(stat.Gid)
		held, err := OpenStateDirectory(path)
		if held != nil {
			noErr(t, held.Close())
		}
		if unsafe && (err == nil || !strings.Contains(err.Error(), "chmod g-w,o-w ") || !strings.Contains(err.Error(), path)) {
			t.Fatalf("mode %o: expected refusal with repair, got %v", mode, err)
		}
		if !unsafe {
			noErr(t, err)
		}
		// Log directories keep their existing ownership-only rule.
		log, err := OpenDirectory(path, false)
		noErr(t, err)
		noErr(t, log.Close())
		after, err := os.Stat(path)
		noErr(t, err)
		if after.Mode() != info.Mode() {
			t.Fatalf("checking mode %o changed it to %o", info.Mode(), after.Mode())
		}
	}
}

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
// error, does not follow a link that a share holds, for any account, even
// when it leads to a local folder of this account: the share's server
// decides where it leads. The share is marked by replacing the filesystem
// check.
func TestOpenDirectoryRefusesAWayThroughAShare(t *testing.T) {
	root := resolveTestPath(t, t.TempDir())
	share, local := filepath.Join(root, "share"), filepath.Join(root, "local")
	noErr(t, os.Mkdir(share, 0o700))
	noErr(t, os.Mkdir(local, 0o700))
	noErr(t, os.Symlink(local, filepath.Join(share, "state")))
	markShare(t, share)
	refusal := "does not follow it"
	if os.Geteuid() == 0 {
		refusal = "run as root uses nothing there"
	}
	for _, create := range []bool{false, true} {
		for _, path := range []string{filepath.Join(share, "state"), filepath.Join(share, "state", "new")} {
			if dir, err := OpenDirectory(path, create); err == nil || !strings.Contains(err.Error(), refusal) {
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

// An account other than root may keep its log in a folder on a share, and
// pass through one on the way, but the state and the way to it must be on
// a local disk: the share's server could read and change the state, or
// rename a folder on the way and put another state there. The share is
// marked by replacing the filesystem check; the local folder inside it
// stands for a local disk mounted there.
func TestLogMayBeOnAShareButNotTheState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root uses nothing on a share")
	}
	root := resolveTestPath(t, t.TempDir())
	share := filepath.Join(root, "share")
	local := filepath.Join(share, "local")
	noErr(t, os.MkdirAll(local, 0o700))
	markShare(t, share)
	for _, path := range []string{filepath.Join(share, "logs"), filepath.Join(local, "logs"), filepath.Join(local, "state")} {
		dir, err := OpenDirectory(path, true)
		if err != nil {
			t.Fatalf("OpenDirectory(%s): %v", path, err)
		}
		dir.Close()
	}
	var refused []error
	for _, path := range []string{filepath.Join(share, "restored"), filepath.Join(local, "restored")} {
		destination, err := OpenStateDestination(path)
		if destination != nil {
			destination.Close()
		}
		refused = append(refused, err)
	}
	for _, open := range []func() (*os.File, error){
		func() (*os.File, error) { return CreateDirectory(filepath.Join(local, "state")) },
		func() (*os.File, error) { return OpenStateDirectory(filepath.Join(local, "state")) },
		func() (*os.File, error) { return CreateDirectory(filepath.Join(share, "state")) },
		func() (*os.File, error) { return CreateDirectory(filepath.Join(share, "state", "nested")) },
		func() (*os.File, error) { return OpenStateDirectory(share) },
	} {
		dir, err := open()
		if dir != nil {
			dir.Close()
		}
		refused = append(refused, err)
	}
	for i, err := range refused {
		if err == nil || !strings.Contains(err.Error(), "only on a local disk") {
			t.Errorf("state on a share, case %d: error=%v, want it refused", i, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(share, "state")); !os.IsNotExist(err) {
		t.Fatalf("a state folder was created on the share: %v", err)
	}
}

// A backup, and the repositories that a restore makes, may be on a share
// for an account other than root, as the repository folder may be. Root
// uses no folder there, and no account puts a state there. The share is
// marked by replacing the filesystem check.
func TestDestinationMayBeOnAShareExceptForRootAndTheState(t *testing.T) {
	root := resolveTestPath(t, t.TempDir())
	share := filepath.Join(root, "share")
	noErr(t, os.Mkdir(share, 0o700))
	markShare(t, share)
	destination, err := OpenDestination(filepath.Join(share, "backup"))
	if os.Geteuid() == 0 {
		if destination != nil {
			destination.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "run as root uses nothing there") {
			t.Fatalf("root's destination on a share: error=%v", err)
		}
	} else {
		noErr(t, err)
		stage, err := destination.CreateStage(".backup.stage")
		if err == nil {
			destination.ReleaseStage()
			err = os.Rename(stage, destination.Path)
		}
		destination.Close()
		noErr(t, err)
	}
	state, err := OpenStateDestination(filepath.Join(share, "state"))
	if state != nil {
		state.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "only on a local disk") && !strings.Contains(err.Error(), "run as root uses nothing there") {
		t.Fatalf("state destination on a share: error=%v", err)
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
	store, err := OpenIn(context.Background(), held, nil)
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

// A folder of root's is not this account's, but running the command as
// root is not the answer: the refusal says to choose another folder.
func TestFolderOfRootIsNotSentToRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns the folder")
	}
	dir, err := OpenDirectory("/", false)
	if dir != nil {
		dir.Close()
	}
	if err == nil || strings.Contains(err.Error(), "run the command as") || !strings.Contains(err.Error(), "choose a folder of this account") {
		t.Fatalf("OpenDirectory(/) error=%v, want a folder of this account asked for", err)
	}
}

// linkTestFolder makes link a symbolic link to target.
func linkTestFolder(t *testing.T, target, link string) {
	t.Helper()
	noErr(t, os.Symlink(target, link))
}
