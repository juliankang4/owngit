//go:build darwin

package state

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const kauthACLNoInherit = 1 << 17

// MkdirPrivate creates an owner-only directory without first exposing an ACL
// inherited from its parent. XNU's KAUTH_ACL_NO_INHERIT flag tells the atomic
// mkdir_extended operation not to replace this empty ACL through inheritance.
// Filesystems without ACL support use ordinary mode-0700 creation. Because
// EINVAL can also mean a malformed filesec, that fallback is accepted only
// after a held no-follow ACL query proves the empty directory inherited no
// permit entry.
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
		return mkdirPrivateWithoutExtendedACL(path)
	}
	return &os.PathError{Op: "mkdir", Path: path, Err: errno}
}

func mkdirPrivateWithoutExtendedACL(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	folder, err := openDirectoryAt(unix.AT_FDCWD, path, path)
	if err != nil {
		return errors.Join(err, os.Remove(path))
	}
	filesec, inspectErr := extendedSecurity(path, folder, 0)
	permit, permitErr := permitEntry(filesec, ^uint32(0))
	closeErr := folder.Close()
	if inspectErr != nil || permitErr != nil || closeErr != nil {
		return errors.Join(fmt.Errorf("inspect newly created private directory: %w", errors.Join(inspectErr, permitErr, closeErr)), os.Remove(path))
	}
	if !permit {
		return nil
	}
	problem := fmt.Errorf("OwnGit could not create %s privately because the storage gives new folders inherited access; remove inheritable entries from the repository root (for example, chmod -N), or run owngit doctor", path)
	return errors.Join(problem, os.Remove(path))
}

var mkdirExtended = func(path uintptr, mode os.FileMode, filesec uintptr) unix.Errno {
	_, _, errno := unix.Syscall6(unix.SYS_MKDIR_EXTENDED, path, uintptr(kauthUIDNone), uintptr(kauthUIDNone), uintptr(mode), filesec, 0)
	return errno
}
