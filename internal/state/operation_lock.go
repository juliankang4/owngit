package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrInstanceRunning = errors.New("OwnGit is running for this state directory")

// AcquireOfflineLock excludes the HTTP server and offline backup or restore
// commands from one another. Commands that intentionally update live owner
// state, such as reset-admin and approve-host, do not take this lock.
func AcquireOfflineLock(directory string) (func(), error) {
	lockPath := filepath.Join(directory, ".offline-operation.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open offline operation lock: %w", err)
	}
	if err := ProtectPrivatePath(lockPath, false); err != nil {
		file.Close()
		return nil, fmt.Errorf("protect offline operation lock: %w", err)
	}
	unlock, err := tryOperationLock(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		unlock()
		_ = file.Close()
	}, nil
}
