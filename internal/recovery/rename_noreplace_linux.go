//go:build linux

package recovery

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameNoReplace(oldPath, newPath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
	// File systems such as NFS refuse the flag with EINVAL, and kernels
	// before 3.15 lack the call.
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) {
		return renameDirectoryOnce(oldPath, newPath, err)
	}
	return err
}
