//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// platformOwnerOf returns the owner of path as a SID string.
func platformOwnerOf(path string) (string, error) {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return "", errors.Join(err, errors.New("no owner"))
	}
	return owner.String(), nil
}

// platformGiveOwnership makes the account sid the owner of root and of
// everything below it that the Administrators group owns. It needs
// administrator rights.
//
// check sees the final path of root, after any link on the way, and its
// owner, and refuses the folder with an error. The walk starts from the
// handle that check was about and opens every entry relative to the open
// folder that lists it, so what changes is what was checked, whatever
// happens to the path afterwards. Links (reparse points) are neither
// changed nor followed, files with more than one name are left as they are,
// and each folder stays open without delete sharing while its contents are
// handled, so nobody can rename it or put a link in its place meanwhile.
// It returns how many owners changed and how many entries it could not
// handle.
func platformGiveOwnership(root, sid string, check func(finalPath, owner string) error) (changed, failed int, err error) {
	account, err := windows.StringToSid(sid)
	if err != nil {
		return 0, 0, err
	}
	administrators, err := windows.StringToSid(administratorsSID)
	if err != nil {
		return 0, 0, err
	}
	// With backup semantics, SeBackupPrivilege lists any folder and
	// SeRestorePrivilege opens any entry for WRITE_OWNER;
	// SeTakeOwnershipPrivilege covers the rest.
	enablePrivileges("SeBackupPrivilege", "SeRestorePrivilege", "SeTakeOwnershipPrivilege")
	name, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, 0, err
	}
	handle, err := windows.CreateFile(name, ownerAccess|windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("open %s: %w", root, err)
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return 0, 0, fmt.Errorf("read %s: %w", root, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return 0, 0, fmt.Errorf("%s is not a folder", root)
	}
	finalPath, err := finalPathOf(handle)
	if err != nil {
		return 0, 0, err
	}
	owner, err := ownerOfHandle(handle)
	if err != nil {
		return 0, 0, err
	}
	if err := check(finalPath, owner.String()); err != nil {
		return 0, 0, err
	}
	walker := ownershipWalker{account: account, administrators: administrators}
	walker.give(handle, owner)
	walker.walk(handle)
	return walker.changed, walker.failed, nil
}

// ownerAccess reads and changes an owner and reads the attributes.
const ownerAccess = windows.READ_CONTROL | windows.WRITE_OWNER | windows.FILE_READ_ATTRIBUTES

type ownershipWalker struct {
	account, administrators *windows.SID
	changed, failed         int
}

// walk handles the entries of the open folder dir, which it lists through
// its handle (FILE_FULL_DIR_INFO records).
func (walker *ownershipWalker) walk(dir windows.Handle) {
	buffer := make([]byte, 64<<10)
	class := uint32(windows.FileFullDirectoryRestartInfo)
	for {
		err := windows.GetFileInformationByHandleEx(dir, class, &buffer[0], uint32(len(buffer)))
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return
		}
		if err != nil {
			walker.failed++
			return
		}
		class = windows.FileFullDirectoryInfo
		for offset := 0; ; {
			record := buffer[offset:]
			attributes := binary.LittleEndian.Uint32(record[56:])
			length := binary.LittleEndian.Uint32(record[60:]) / 2
			name := unsafe.Slice((*uint16)(unsafe.Pointer(&record[68])), length)
			if !(length == 1 && name[0] == '.' || length == 2 && name[0] == '.' && name[1] == '.') {
				walker.entry(dir, name, attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0)
			}
			next := binary.LittleEndian.Uint32(record[0:])
			if next == 0 {
				break
			}
			offset += int(next)
		}
	}
}

// entry handles the entry name of the open folder dir. listed tells
// whether the listing called it a folder; only a folder is opened so that
// it can be listed, and the open entry decides what it is.
func (walker *ownershipWalker) entry(dir windows.Handle, name []uint16, listed bool) {
	access := uint32(ownerAccess | windows.SYNCHRONIZE)
	if listed {
		access |= windows.FILE_LIST_DIRECTORY
	}
	object := windows.NTUnicodeString{Length: uint16(2 * len(name)), MaximumLength: uint16(2 * len(name)), Buffer: &name[0]}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: dir, ObjectName: &object}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	if windows.NtCreateFile(&handle, access, &attributes, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0) != nil {
		walker.failed++
		return
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &info) != nil {
		walker.failed++
		return
	}
	reparse := info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	directory := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if reparse || !directory && info.NumberOfLinks != 1 {
		return
	}
	if owner, err := ownerOfHandle(handle); err != nil {
		walker.failed++
	} else {
		walker.give(handle, owner)
	}
	switch {
	case directory && listed:
		walker.walk(handle)
	case directory:
		// It became a folder after the listing; it was opened without the
		// right to list it.
		walker.failed++
	}
}

// give changes the owner of handle to the account when it is the
// Administrators group.
func (walker *ownershipWalker) give(handle windows.Handle, owner *windows.SID) {
	if !owner.Equals(walker.administrators) {
		return
	}
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, walker.account, nil, nil, nil); err != nil {
		walker.failed++
		return
	}
	walker.changed++
}

func ownerOfHandle(handle windows.Handle) (*windows.SID, error) {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return nil, errors.Join(err, errors.New("no owner"))
	}
	return owner.Copy()
}

// finalPathOf returns the path of the open file with every link resolved,
// as C:\... or \\server\share\...
func finalPathOf(handle windows.Handle) (string, error) {
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	path := windows.UTF16ToString(buffer[:n])
	if rest, found := strings.CutPrefix(path, `\\?\UNC\`); found {
		return `\\` + rest, nil
	}
	return strings.TrimPrefix(path, `\\?\`), nil
}

// enablePrivileges enables privileges this process holds; the others are
// left out.
func enablePrivileges(names ...string) {
	var token windows.Token
	if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token) != nil {
		return
	}
	defer token.Close()
	for _, name := range names {
		var luid windows.LUID
		text, err := windows.UTF16PtrFromString(name)
		if err != nil || windows.LookupPrivilegeValue(nil, text, &luid) != nil {
			continue
		}
		privileges := windows.Tokenprivileges{PrivilegeCount: 1}
		privileges.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		_ = windows.AdjustTokenPrivileges(token, false, &privileges, 0, nil, nil)
	}
}
