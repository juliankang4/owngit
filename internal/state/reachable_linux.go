//go:build linux

package state

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	posixACLUserObj = 0x01
	posixACLOther   = 0x20
	posixACLSearch  = 0x01
)

func openReachableDirectoryAt(dirfd int, name, path string) (*os.File, error) {
	return openDirectoryAt(dirfd, name, path)
}

func groupOrAccessListAllowsOtherSearch(file *os.File, info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, errors.New("owning group is unavailable")
	}
	heldPath := "/proc/self/fd/" + strconv.FormatUint(uint64(file.Fd()), 10)
	entries, err := readPosixACL(func(buffer []byte) (int, error) {
		size, err := unix.Getxattr(heldPath, "system.posix_acl_access", buffer)
		runtime.KeepAlive(file)
		return size, err
	})
	if err != nil {
		return false, fmt.Errorf("read access list through held directory %s: %w", heldPath, err)
	}
	if len(entries) == 0 {
		return info.Mode().Perm()&0o010 != 0 && !OwnPrivateGroup(stat.Gid) && !rootEquivalentGroup(stat.Gid), nil
	}

	mask := uint16(posixACLSearch)
	for offset := 4; offset < len(entries); offset += 8 {
		if binary.LittleEndian.Uint16(entries[offset:]) == posixACLMask {
			mask = binary.LittleEndian.Uint16(entries[offset+2:])
		}
	}
	for offset := 4; offset < len(entries); offset += 8 {
		tag := binary.LittleEndian.Uint16(entries[offset:])
		permissions := binary.LittleEndian.Uint16(entries[offset+2:])
		id := binary.LittleEndian.Uint32(entries[offset+4:])
		effective := permissions
		if tag == posixACLUser || tag == posixACLGroup || tag == posixACLGroupObj {
			effective &= mask
		}
		if effective&posixACLSearch == 0 {
			continue
		}
		switch {
		case tag == posixACLUserObj:
			continue
		case tag == posixACLUser && id != 0 && int(id) != os.Geteuid():
			return true, nil
		case tag == posixACLGroup && !OwnPrivateGroup(id) && !rootEquivalentGroup(id):
			return true, nil
		case tag == posixACLGroupObj && !OwnPrivateGroup(stat.Gid) && !rootEquivalentGroup(stat.Gid):
			return true, nil
		case tag == posixACLOther:
			return true, nil
		}
	}
	return false, nil
}
