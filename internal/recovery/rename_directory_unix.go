//go:build darwin || linux

package recovery

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"golang.org/x/sys/unix"
)

// renameDirectoryOnce publishes the directory oldPath at newPath where the
// file system cannot rename without replacing (unsupported is its
// refusal), such as exFAT on macOS or NFS on Linux. It creates newPath
// with mkdir, which fails when anything is there, then moves what oldPath
// holds into that new, empty, private folder through handles of both, the
// manifest last, and removes the empty oldPath. So nothing that existed
// before is replaced. Only a process that can write in the parent could
// replace the new folder before it is opened; on a file system that
// enforces owners that is this account, and on exFAT or FAT any account
// that can write the volume can already change every file on it. A failure
// on the way names both folders.
func renameDirectoryOnce(oldPath, newPath string, unsupported error) error {
	source, err := openDirectoryNoFollow(oldPath)
	if err != nil {
		return unsupported
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
	// The manifest last: a folder without it is not a backup.
	sort.SliceStable(names, func(left, right int) bool { return names[right] == manifestName && names[left] != manifestName })
	for _, name := range names {
		if err := unix.Renameat(int(source.Fd()), name, int(target.Fd()), name); err != nil {
			return fmt.Errorf("move %s from %s into %s: %w", name, oldPath, newPath, err)
		}
	}
	return os.Remove(oldPath)
}

func openDirectoryNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
