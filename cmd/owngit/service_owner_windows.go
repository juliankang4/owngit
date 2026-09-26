//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
// owner, and refuses the folder with an error. Links (reparse points) are
// neither changed nor followed, files with more than one name are left as
// they are, and each folder stays open without delete sharing while its
// contents are handled, so nobody can rename it or put a link in its place
// meanwhile. It returns how many owners changed and how many entries it
// could not handle.
func platformGiveOwnership(root, sid string, check func(finalPath, owner string) error) (changed, failed int, err error) {
	account, err := windows.StringToSid(sid)
	if err != nil {
		return 0, 0, err
	}
	administrators, err := windows.StringToSid(administratorsSID)
	if err != nil {
		return 0, 0, err
	}
	// SeRestorePrivilege opens any file for WRITE_OWNER with backup
	// semantics; SeTakeOwnershipPrivilege covers the rest.
	enablePrivileges("SeRestorePrivilege", "SeTakeOwnershipPrivilege")
	handle, info, err := openForOwner(root)
	if err != nil {
		return 0, 0, err
	}
	defer windows.CloseHandle(handle)
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
	walker.walk(root)
	return walker.changed, walker.failed, nil
}

type ownershipWalker struct {
	account, administrators *windows.SID
	changed, failed         int
}

// walk handles the entries of the open folder dir.
func (walker *ownershipWalker) walk(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		walker.failed++
		return
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		handle, info, err := openForOwner(path)
		if err != nil {
			walker.failed++
			continue
		}
		reparse := info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
		directory := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		if !reparse && (directory || info.NumberOfLinks == 1) {
			if owner, err := ownerOfHandle(handle); err != nil {
				walker.failed++
			} else {
				walker.give(handle, owner)
			}
			if directory {
				walker.walk(path)
			}
		}
		windows.CloseHandle(handle)
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

// openForOwner opens path itself, not what a link points to, to read and
// change its owner, and shares it with no one who would delete or rename
// it.
func openForOwner(path string) (windows.Handle, windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, info, err
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_OWNER|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, info, fmt.Errorf("open %s: %w", path, err)
	}
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return 0, info, fmt.Errorf("read %s: %w", path, err)
	}
	return handle, info, nil
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
