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

// ownershipEnforcedHere reports whether this computer's kernel keeps the
// owners and modes of the filesystem that holds path. On a network
// filesystem the server decides them, and on a FUSE filesystem the program
// behind it does; either can let another account change what they show as
// root's folder.
func ownershipEnforcedHere(path string) (bool, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return false, err
	}
	return ownershipEnforcedOn(uint64(stat.Type)), nil
}

// ownershipEnforcedOn reports it for the filesystem type magic.
func ownershipEnforcedOn(magic uint64) bool {
	switch magic {
	case uint64(unix.NFS_SUPER_MAGIC), uint64(unix.CIFS_SUPER_MAGIC), uint64(unix.SMB_SUPER_MAGIC),
		uint64(unix.SMB2_SUPER_MAGIC), uint64(unix.V9FS_MAGIC), uint64(unix.CEPH_SUPER_MAGIC),
		uint64(unix.AFS_SUPER_MAGIC), uint64(unix.AFS_FS_MAGIC), uint64(unix.CODA_SUPER_MAGIC),
		uint64(unix.NCP_SUPER_MAGIC), uint64(unix.FUSE_SUPER_MAGIC):
		return false
	}
	return true
}
