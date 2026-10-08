package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"owngit/internal/statepath"
)

var ErrInstanceRunning = errors.New("OwnGit is running for this state directory")

// AcquireExclusiveFileLock holds a nonblocking process lock on one private
// file until the returned release function is called.
func AcquireExclusiveFileLock(lockPath string) (func(), error) {
	_, release, err := AcquireExclusiveFileLockHandle(lockPath)
	return release, err
}

// AcquireExclusiveFileLockHandle is AcquireExclusiveFileLock with the locked
// open file returned. A caller that must revalidate the lock path later
// compares the path against this handle instead of trusting a fresh lookup,
// which can silently point at a replaced file.
//
// The lock file is opened through its folder's handle with OpenOwnFile, so a
// link or another account's file at its name is refused before the lock
// makes it private. The folder is not checked further: a lock in the state
// directory is taken once OpenDirectory accepted the way to it, and a
// repository folder may be on a share, or be shared with a group, whose
// other writers are trusted with the repositories in it anyway. The folder
// is found by following its path, because the owner may reach a repository
// folder through a link of their own, such as ~/git to a folder on another
// disk; the folder that path leads to is then held, and the lock file is
// opened in it.
func AcquireExclusiveFileLockHandle(lockPath string) (*os.File, func(), error) {
	file, err := OpenLockFile(lockPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open exclusive operation lock: %w", err)
	}
	return acquireExclusiveFileLock(file)
}

// acquireExclusiveFileLockIn is AcquireExclusiveFileLockHandle for the
// lock file name in the held directory dir.
func acquireExclusiveFileLockIn(dir *os.File, name string) (*os.File, func(), error) {
	file, err := openLockFileIn(dir, name)
	if err != nil {
		return nil, nil, fmt.Errorf("open exclusive operation lock: %w", err)
	}
	return acquireExclusiveFileLock(file)
}

// OpenLockFile opens, or creates, the lock file at path for a caller that
// locks it itself, as the lock helpers here do, and makes it private. It is
// opened through its folder's handle with OpenOwnFile, and protected only
// once OpenOwnFile accepted it.
func OpenLockFile(path string) (*os.File, error) {
	dir, err := openFolder(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return openLockFileIn(dir, filepath.Base(path))
}

// openLockFileIn is OpenLockFile for the lock file name in the held
// directory dir.
func openLockFileIn(dir *os.File, name string) (*os.File, error) {
	file, err := OpenOwnFile(dir, name, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	if err := ProtectPrivateHandle(file, false); err != nil {
		file.Close()
		return nil, fmt.Errorf("protect %s: %w", file.Name(), err)
	}
	return file, nil
}

// AcquireExclusivePrivateFileLockHandle locks an existing private file without
// rewriting its permissions. Validation and later reads use the returned handle,
// so a pathname replacement cannot change which file was accepted.
func AcquireExclusivePrivateFileLockHandle(path string) (*os.File, func(), error) {
	dir, err := openFolder(filepath.Dir(path))
	if err != nil {
		return nil, nil, fmt.Errorf("open exclusive private-file lock: %w", err)
	}
	defer dir.Close()
	file, err := OpenOwnFile(dir, filepath.Base(path), os.O_RDWR)
	if err != nil {
		return nil, nil, fmt.Errorf("open exclusive private-file lock: %w", err)
	}
	if err := ValidatePrivateFileHandle(file); err != nil {
		file.Close()
		return nil, nil, fmt.Errorf("validate exclusive private-file lock: %w", err)
	}
	return acquireExclusiveFileLock(file)
}

func acquireExclusiveFileLock(file *os.File) (*os.File, func(), error) {
	unlock, err := tryOperationLock(file)
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, func() {
		unlock()
		_ = file.Close()
	}, nil
}

// offlineLockFile is the lock file of AcquireOfflineLock.
const offlineLockFile = statepath.OfflineLock

// AcquireOfflineLock excludes the HTTP server and offline backup or restore
// commands from one another. Commands that intentionally update live owner
// state, such as reset-admin and approve-host, do not take this lock.
func AcquireOfflineLock(directory string) (func(), error) {
	release, err := AcquireExclusiveFileLock(filepath.Join(directory, offlineLockFile))
	if err != nil {
		return nil, fmt.Errorf("acquire offline operation lock: %w", err)
	}
	return release, nil
}

// AcquireOfflineLockIn is AcquireOfflineLock in the held state directory
// that CreateDirectory or OpenDirectory returned.
func AcquireOfflineLockIn(held *os.File) (func(), error) {
	_, release, err := acquireExclusiveFileLockIn(held, offlineLockFile)
	if err != nil {
		return nil, fmt.Errorf("acquire offline operation lock: %w", err)
	}
	return release, nil
}
