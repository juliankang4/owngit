//go:build darwin || linux

package state

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func tryOperationLock(file *os.File) (func(), error) {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrInstanceRunning
		}
		return nil, fmt.Errorf("lock offline operation: %w", err)
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }, nil
}
