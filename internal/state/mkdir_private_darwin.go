//go:build darwin

package state

import (
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const kauthACLNoInherit = 1 << 17

// MkdirPrivate creates an owner-only directory without first exposing an ACL
// inherited from its parent. XNU's KAUTH_ACL_NO_INHERIT flag tells the atomic
// mkdir_extended operation not to replace this empty ACL through inheritance.
// Filesystems without ACL support use ordinary mode-0700 creation, whose mode
// is the established protection there.
func MkdirPrivate(path string) error {
	name, err := unix.BytePtrFromString(path)
	if err != nil {
		return err
	}
	// KAUTH_FILESEC_SIZE(0): magic, owner GUID, group GUID, entry count and
	// flags. Zero owner and group GUIDs leave ownership to mkdir; zero entries
	// is an empty ACL rather than KAUTH_FILESEC_NOACL.
	filesec := make([]byte, kauthEntriesOffset)
	binary.NativeEndian.PutUint32(filesec, kauthFilesecMagic)
	binary.NativeEndian.PutUint32(filesec[kauthEntriesOffset-8:], 0)
	binary.NativeEndian.PutUint32(filesec[kauthEntriesOffset-4:], kauthACLNoInherit)
	errno := mkdirExtended(uintptr(unsafe.Pointer(name)), 0o700, uintptr(unsafe.Pointer(&filesec[0])))
	runtime.KeepAlive(name)
	runtime.KeepAlive(filesec)
	if errno == 0 {
		return nil
	}
	if errors.Is(errno, unix.EINVAL) || errors.Is(errno, unix.ENOTSUP) || errors.Is(errno, unix.EOPNOTSUPP) {
		return os.Mkdir(path, 0o700)
	}
	return &os.PathError{Op: "mkdir", Path: path, Err: errno}
}

var mkdirExtended = func(path uintptr, mode os.FileMode, filesec uintptr) unix.Errno {
	_, _, errno := unix.Syscall6(unix.SYS_MKDIR_EXTENDED, path, uintptr(kauthUIDNone), uintptr(kauthUIDNone), uintptr(mode), filesec, 0)
	return errno
}
