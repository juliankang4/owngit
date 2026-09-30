//go:build linux

package recovery

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func renameNoReplace(oldPath, newPath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
}

// exclusiveRenameUnsupported says that err is the refusal of a file
// system that cannot rename exclusively: NFS refuses the flag with EINVAL,
// and kernels before 3.15 lack the call.
func exclusiveRenameUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS)
}

// fileSystemName names the file system that holds dir.
func fileSystemName(dir string) string {
	var stat unix.Statfs_t
	if unix.Statfs(dir, &stat) != nil {
		return "unknown"
	}
	switch uint32(stat.Type) {
	case unix.NFS_SUPER_MAGIC:
		return "nfs"
	case unix.EXFAT_SUPER_MAGIC:
		return "exfat"
	case unix.MSDOS_SUPER_MAGIC:
		return "vfat"
	case unix.CIFS_SUPER_MAGIC, unix.SMB2_SUPER_MAGIC:
		return "cifs"
	case unix.FUSE_SUPER_MAGIC:
		return "fuse"
	}
	return fmt.Sprintf("type %#x", uint32(stat.Type))
}
