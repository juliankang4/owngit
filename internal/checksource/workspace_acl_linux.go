//go:build linux

package checksource

import (
	"encoding/binary"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// POSIX access list entries as Linux stores them in an extended attribute.
const (
	posixACLVersion  = 2
	posixACLUser     = 0x02
	posixACLGroupObj = 0x04
	posixACLGroup    = 0x08
	posixACLMask     = 0x10
	posixACLWrite    = 0x02
)

// ancestorACLFix refuses a POSIX access list that lets an account other than
// this one or root, or a group other than this account's private group,
// write. Entries for named accounts and groups only take effect through the
// mask, which the mode shows as group permissions, so a directory without
// group write needs no further check. Removing group write clears the mask.
func ancestorACLFix(path string, info os.FileInfo) (string, error) {
	if info.Mode().Perm()&0o020 == 0 {
		return "", nil
	}
	entries, err := posixAccessACL(path)
	if err != nil || len(entries) == 0 {
		return "", err
	}
	mask := uint16(posixACLWrite)
	for offset := 4; offset < len(entries); offset += 8 {
		if binary.LittleEndian.Uint16(entries[offset:]) == posixACLMask {
			mask = binary.LittleEndian.Uint16(entries[offset+2:])
		}
	}
	gid := info.Sys().(*unix.Stat_t).Gid
	for offset := 4; offset < len(entries); offset += 8 {
		tag := binary.LittleEndian.Uint16(entries[offset:])
		permissions := binary.LittleEndian.Uint16(entries[offset+2:])
		id := binary.LittleEndian.Uint32(entries[offset+4:])
		if permissions&mask&posixACLWrite == 0 {
			continue
		}
		switch {
		case tag == posixACLUser && id != 0 && int(id) != os.Geteuid(),
			tag == posixACLGroup && !ownPrivateGroup(id),
			tag == posixACLGroupObj && !ownPrivateGroup(gid):
			return "chmod g-w,o-w " + quoteWorkspacePath(path), nil
		}
	}
	return "", nil
}

// rootACLFix accepts the workspace root: its owner-only mode sets the mask to
// no access, so named entries in its access list no longer apply.
func rootACLFix(string) (string, error) {
	return "", nil
}

// posixAccessACL reads the access list of path, without following a final
// link. It returns nothing when there is none.
func posixAccessACL(path string) ([]byte, error) {
	buffer := make([]byte, 4096)
	for {
		size, err := unix.Lgetxattr(path, "system.posix_acl_access", buffer)
		switch {
		case errors.Is(err, unix.ENODATA), errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EOPNOTSUPP):
			return nil, nil
		case errors.Is(err, unix.ERANGE) && len(buffer) < 1<<20:
			buffer = make([]byte, 2*len(buffer))
			continue
		case err != nil:
			return nil, err
		}
		entries := buffer[:size]
		if size < 4 || (size-4)%8 != 0 || binary.LittleEndian.Uint32(entries) != posixACLVersion {
			return nil, errors.New("unrecognized access list")
		}
		return entries, nil
	}
}
