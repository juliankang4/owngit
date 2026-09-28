//go:build !windows

package state

import (
	"os"
	"path/filepath"
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
