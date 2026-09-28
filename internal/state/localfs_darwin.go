//go:build darwin

package state

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// ownershipEnforced reports whether this computer's kernel keeps the owners
// and modes of the filesystem that holds the open directory. macOS marks
// such a filesystem local; the owners and modes of a network share, or of a
// FUSE filesystem that is not marked local, come from its server or program,
// which can let another account change what they show as root's folder.
func ownershipEnforced(dir *os.File) (bool, error) {
	var stat unix.Statfs_t
	err := unix.Fstatfs(int(dir.Fd()), &stat)
	runtime.KeepAlive(dir)
	if err != nil {
		return false, err
	}
	return ownershipEnforcedWith(uint64(stat.Flags)), nil
}

// ownershipEnforcedWith reports it for the mount flags.
func ownershipEnforcedWith(flags uint64) bool {
	return flags&uint64(unix.MNT_LOCAL) != 0
}
