//go:build darwin

package recovery

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameNoReplace(oldPath, newPath string) error {
	return unix.RenamexNp(oldPath, newPath, unix.RENAME_EXCL)
}

// exclusiveRenameUnsupported says that err is the refusal of a file
// system, such as exFAT or FAT, that cannot rename exclusively.
func exclusiveRenameUnsupported(err error) bool {
	return errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP)
}

// fileSystemName names the file system that holds dir, such as exfat.
func fileSystemName(dir string) string {
	var stat unix.Statfs_t
	if unix.Statfs(dir, &stat) != nil {
		return "unknown"
	}
	return unix.ByteSliceToString(stat.Fstypename[:])
}
