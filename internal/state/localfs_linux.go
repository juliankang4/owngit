//go:build linux

package state

import (
	"errors"

	"golang.org/x/sys/unix"
)

func ensureLocalStateFilesystem(path string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return err
	}
	switch uint64(stat.Type) {
	case uint64(unix.NFS_SUPER_MAGIC), uint64(unix.CIFS_SUPER_MAGIC), uint64(unix.SMB2_SUPER_MAGIC):
		return errors.New("state directory must be on a host-local filesystem")
	default:
		return nil
	}
}
