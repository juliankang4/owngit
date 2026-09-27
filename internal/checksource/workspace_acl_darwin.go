//go:build darwin

package checksource

import (
	"encoding/binary"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// macOS access list entries (sys/kauth.h).
const (
	kauthFilesecMagic = 0x012cc16d
	kauthFilesecNoACL = 0xffffffff
	kauthACEKindMask  = 0xf
	kauthACEPermit    = 1
	// Rights that let a holder add, remove or rename entries, remove the
	// directory itself, or change its owner or access list.
	kauthChangeRights = 1<<2 | 1<<4 | 1<<5 | 1<<6 | 1<<12 | 1<<13 | 1<<21 | 1<<23
	// kauth_filesec: magic, owner and group GUIDs, then the ACL entry count
	// and flags; each entry is a GUID, flags and rights.
	kauthEntriesOffset = 4 + 16 + 16 + 4 + 4
	kauthEntrySize     = 16 + 4 + 4
)

// ancestorACLFix refuses any macOS access list entry that allows changes. The
// entry names an account by GUID, and mapping that GUID needs the system
// library, so an entry for this account is refused too.
func ancestorACLFix(path string, _ os.FileInfo) (string, error) {
	return macOSACLFix(path)
}

// rootACLFix applies the same rule to the workspace root, because the
// owner-only mode leaves macOS access list entries in place.
func rootACLFix(path string) (string, error) {
	return macOSACLFix(path)
}

func macOSACLFix(path string) (string, error) {
	filesec, err := extendedSecurity(path)
	if err != nil || len(filesec) == 0 {
		return "", err
	}
	if len(filesec) < kauthEntriesOffset || binary.NativeEndian.Uint32(filesec) != kauthFilesecMagic {
		return "", errors.New("unrecognized access list")
	}
	count := binary.NativeEndian.Uint32(filesec[kauthEntriesOffset-8:])
	if count == kauthFilesecNoACL {
		return "", nil
	}
	if uint64(count) > uint64(len(filesec)-kauthEntriesOffset)/kauthEntrySize {
		return "", errors.New("truncated access list")
	}
	for index := range int(count) {
		entry := filesec[kauthEntriesOffset+index*kauthEntrySize:]
		flags := binary.NativeEndian.Uint32(entry[16:])
		rights := binary.NativeEndian.Uint32(entry[20:])
		if flags&kauthACEKindMask == kauthACEPermit && rights&kauthChangeRights != 0 {
			return "chmod -N " + quoteWorkspacePath(path), nil
		}
	}
	return "", nil
}

// extendedSecurity returns the kauth_filesec of path, without following a
// final link, or nothing when it has no access list. golang.org/x/sys/unix
// does not wrap getattrlist, so it is called directly.
func extendedSecurity(path string) ([]byte, error) {
	name, err := unix.BytePtrFromString(path)
	if err != nil {
		return nil, err
	}
	request := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY}
	buffer := make([]byte, 64<<10)
	_, _, errno := unix.Syscall6(unix.SYS_GETATTRLIST, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&request)),
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), unix.FSOPT_NOFOLLOW, 0)
	if errno != 0 {
		return nil, errno
	}
	// The buffer holds its total length, then an attribute reference: an
	// offset from the reference itself and a length.
	total := binary.NativeEndian.Uint32(buffer)
	if total < 12 || total > uint32(len(buffer)) {
		return nil, errors.New("unexpected attribute buffer")
	}
	offset := int32(binary.NativeEndian.Uint32(buffer[4:]))
	length := binary.NativeEndian.Uint32(buffer[8:])
	start := 4 + int64(offset)
	if length == 0 {
		return nil, nil
	}
	if offset < 0 || start+int64(length) > int64(total) {
		return nil, errors.New("unexpected attribute buffer")
	}
	return buffer[start : start+int64(length)], nil
}
