//go:build !windows

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

// openFolder opens the directory at path, following links on the way, for
// OpenOwnFile. The folder is then the one this handle holds, whatever path
// names later.
func openFolder(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, searchOnly|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

// OpenOwnFile opens the file name in the held directory dir, with flag
// os.O_RDONLY, os.O_WRONLY or os.O_RDWR, and os.O_APPEND, without following
// a link at that name. With os.O_CREATE it creates the file, private to this
// account, when the name is free, and never opens what another process put
// there in between. A file that exists must be a regular file of this
// account with no other name: another account could link one of its own
// files, or a file of this account that it can name, into a folder that it
// can write, and a caller that then truncates, locks or protects the file
// would change that file. OpenOwnFile never truncates; a caller truncates
// the returned file once it is known. It does not wait on a named pipe put
// at the name.
func OpenOwnFile(dir *os.File, name string, flag int) (*os.File, error) {
	path := filepath.Join(dir.Name(), name)
	access := flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR | os.O_APPEND)
	open := func(extra int) (int, error) {
		descriptor, err := unix.Openat(int(dir.Fd()), name, access|extra|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
		runtime.KeepAlive(dir)
		return descriptor, err
	}
	descriptor, err := open(0)
	// Another opener can create the name between the two opens; its file
	// is then opened and checked like any existing one.
	for attempt := 0; errors.Is(err, unix.ENOENT) && flag&os.O_CREATE != 0 && attempt < createAttempts; attempt++ {
		if descriptor, err = open(unix.O_CREAT | unix.O_EXCL); errors.Is(err, unix.EEXIST) {
			descriptor, err = open(0)
		}
	}
	if errors.Is(err, unix.EISDIR) {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if err := requireOwnFile(descriptor, path); err != nil {
		unix.Close(descriptor)
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

// requireOwnFile refuses an open file that OpenOwnFile must not use, and
// makes one that it may use wait on reads and writes again.
func requireOwnFile(descriptor int, path string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(descriptor, &stat); err != nil {
		return &os.PathError{Op: "stat", Path: path, Err: err}
	}
	switch {
	case stat.Mode&unix.S_IFMT != unix.S_IFREG:
		return fmt.Errorf("%s is not a regular file", path)
	case int(stat.Uid) != os.Geteuid():
		return fmt.Errorf("%s belongs to another account", path)
	case stat.Nlink != 1:
		return fmt.Errorf("%s has another name as well, so it may be another file", path)
	}
	if err := unix.SetNonblock(descriptor, false); err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	return nil
}

// RenameOwnFile renames the entry from in the held directory dir to the
// name to there, replacing what is there, without following a link at
// either name.
func RenameOwnFile(dir *os.File, from, to string) error {
	err := unix.Renameat(int(dir.Fd()), from, int(dir.Fd()), to)
	runtime.KeepAlive(dir)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: filepath.Join(dir.Name(), from), New: filepath.Join(dir.Name(), to), Err: err}
	}
	return nil
}

// removeOwnFile removes the file name in the held directory dir once
// OpenOwnFile accepted it as this account's own regular file.
func removeOwnFile(dir *os.File, name string) error {
	file, err := OpenOwnFile(dir, name, os.O_RDONLY)
	if err != nil {
		return err
	}
	file.Close()
	err = unix.Unlinkat(int(dir.Fd()), name, 0)
	runtime.KeepAlive(dir)
	if err != nil {
		return &os.PathError{Op: "remove", Path: filepath.Join(dir.Name(), name), Err: err}
	}
	return nil
}
