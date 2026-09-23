package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
func AcquireExclusiveFileLockHandle(lockPath string) (*os.File, func(), error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open exclusive operation lock: %w", err)
	}
	if err := ProtectPrivatePath(lockPath, false); err != nil {
		file.Close()
		return nil, nil, fmt.Errorf("protect exclusive operation lock: %w", err)
	}
	return acquireExclusiveFileLock(file)
}

// AcquireExclusivePrivateFileLockHandle locks an existing private file without
// rewriting its permissions. Validation and later reads use the returned handle,
// so a pathname replacement cannot change which file was accepted.
func AcquireExclusivePrivateFileLockHandle(path string) (*os.File, func(), error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
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

// AcquireOfflineLock excludes the HTTP server and offline backup or restore
// commands from one another. Commands that intentionally update live owner
// state, such as reset-admin and approve-host, do not take this lock.
func AcquireOfflineLock(directory string) (func(), error) {
	release, err := AcquireExclusiveFileLock(filepath.Join(directory, ".offline-operation.lock"))
	if err != nil {
		return nil, fmt.Errorf("acquire offline operation lock: %w", err)
	}
	return release, nil
}
