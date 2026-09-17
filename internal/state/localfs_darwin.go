//go:build darwin

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
	if uint64(stat.Flags)&uint64(unix.MNT_LOCAL) == 0 {
		return errors.New("state directory must be on a host-local filesystem")
	}
	return nil
}
