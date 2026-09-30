//go:build darwin || linux

package recovery

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// publishBackup publishes the backup stage at output, never over anything
// that exists. Where the file system cannot rename without replacing, such
// as exFAT on macOS or NFS on Linux, it moves the backup into a new folder
// instead (moveIntoNewFolder). Only backups are published so: a restore
// needs the exclusive rename (requireExclusiveRename).
func publishBackup(stage, output string) error {
	err := renameNoReplace(stage, output)
	if exclusiveRenameUnsupported(err) {
		return moveIntoNewFolder(stage, output)
	}
	return err
}

// moveIntoNewFolder creates newPath with mkdir, which fails when anything
// is there, an empty folder included, and moves what the backup stage
// oldPath holds into that new, empty, private folder through handles of
// both. Everything but the manifest goes first; each moved entry and both
// folders are synchronized, then the manifest is moved and both folders
// are synchronized again, and the empty stage is removed. A companion
// (isCompanion) moves after its file, which the file system usually moves
// it with: moving it first would lose it. A folder without
// a manifest is no backup, so one left by a stop on the way is never taken
// for one. Nothing that existed before is replaced. Only a process that
// can write in the parent could replace the new folder before it is
// opened; on a file system that enforces owners that is this account, and
// on exFAT or FAT any account that can write the volume can already change
// every file on it. A failure on the way names both folders.
func moveIntoNewFolder(oldPath, newPath string) error {
	source, err := openDirectoryNoFollow(oldPath)
	if err != nil {
		return err
	}
	defer source.Close()
	names, err := source.Readdirnames(-1)
	if err != nil {
		return err
	}
	if err := os.Mkdir(newPath, 0o700); err != nil {
		return err
	}
	target, err := openDirectoryNoFollow(newPath)
	if err != nil {
		return err
	}
	defer target.Close()
	if _, err := target.Readdirnames(1); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s was not the empty folder just created", newPath)
	}
	move := func(name string) error {
		if err := unix.Renameat(int(source.Fd()), name, int(target.Fd()), name); err != nil {
			return fmt.Errorf("move %s from %s into %s: %w", name, oldPath, newPath, err)
		}
		return nil
	}
	syncBoth := func() error {
		if err := target.Sync(); err != nil {
			return fmt.Errorf("synchronize %s: %w", newPath, err)
		}
		if err := source.Sync(); err != nil {
			return fmt.Errorf("synchronize %s: %w", oldPath, err)
		}
		return nil
	}
	listed := make(map[string]bool, len(names))
	for _, name := range names {
		listed[name] = true
	}
	var companions []string
	for _, name := range names {
		if isCompanion(name, func(base string) bool { return listed[base] }) {
			companions = append(companions, name)
			continue
		}
		if name == manifestName {
			continue
		}
		if err := move(name); err != nil {
			return err
		}
		if err := syncEntry(target, name); err != nil {
			return err
		}
	}
	if err := syncBoth(); err != nil {
		return err
	}
	if listed[manifestName] {
		if err := move(manifestName); err != nil {
			return err
		}
	}
	for _, name := range companions {
		if err := move(name); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	if err := syncBoth(); err != nil {
		return err
	}
	return os.Remove(oldPath)
}

// syncEntry synchronizes the file or folder name in the open folder dir,
// not following a link.
func syncEntry(dir *os.File, name string) error {
	path := filepath.Join(dir.Name(), name)
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("synchronize %s: %w", path, err)
	}
	return nil
}

// requireExclusiveRename refuses dir, where a restore would publish,
// before any work when its file system cannot rename without replacing: a
// restore publishes its folders only with the exclusive rename. It renames
// a new empty folder there once to find out.
func requireExclusiveRename(dir string) error {
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	probe := filepath.Join(dir, ".owngit-rename-check-"+suffix)
	if err := os.Mkdir(probe, 0o700); err != nil {
		return err
	}
	renamed := probe + "-renamed"
	err = renameNoReplace(probe, renamed)
	if err == nil {
		return os.Remove(renamed)
	}
	_ = os.Remove(probe)
	if exclusiveRenameUnsupported(err) {
		return fmt.Errorf("%s is on a file system (%s) that cannot rename a folder without replacing, which a restore needs; restore to a folder on another disk", dir, fileSystemName(dir))
	}
	return err
}

func openDirectoryNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
