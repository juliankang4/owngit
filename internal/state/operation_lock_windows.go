//go:build windows

package state

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func tryOperationLock(file *os.File) (func(), error) {
	handle := windows.Handle(file.Fd())
	var overlapped windows.Overlapped
	err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrInstanceRunning
		}
		return nil, fmt.Errorf("lock offline operation: %w", err)
	}
	return func() { _ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped) }, nil
}
