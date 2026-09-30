//go:build darwin

package recovery

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameNoReplace(oldPath, newPath string) error {
	err := unix.RenamexNp(oldPath, newPath, unix.RENAME_EXCL)
	// File systems such as exFAT and FAT cannot rename exclusively.
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
		return renameDirectoryOnce(oldPath, newPath, err)
	}
	return err
}
