//go:build darwin || linux

package recovery

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// diskFreeSpace returns the bytes that this account may still write on
// the file system that holds dir, and true as it can tell.
func diskFreeSpace(dir string) (uint64, bool, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return 0, false, err
	}
	return stat.Bavail * uint64(stat.Bsize), true, nil
}

func diskFullError(err error) bool { return errors.Is(err, syscall.ENOSPC) }
