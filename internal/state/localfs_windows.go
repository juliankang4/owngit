//go:build windows

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func ensureLocalStateFilesystem(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if windowsNetworkPath(absolute) {
		return errors.New("state directory must not use a network share")
	}
	resolved, err := finalWindowsPath(absolute)
	if err != nil {
		return fmt.Errorf("resolve state directory target: %w", err)
	}
	if windowsNetworkPath(resolved) {
		return errors.New("state directory reparse target must not use a network share")
	}
	resolvedPath, err := windows.UTF16PtrFromString(resolved)
	if err != nil {
		return err
	}
	volume := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(resolvedPath, &volume[0], uint32(len(volume))); err != nil {
		return fmt.Errorf("resolve state directory volume: %w", err)
	}
	volumePath := windows.UTF16ToString(volume)
	if windowsNetworkPath(volumePath) {
		return errors.New("state directory resolved volume must not be remote")
	}
	root, err := windows.UTF16PtrFromString(volumePath)
	if err != nil {
		return err
	}
	switch driveType := windows.GetDriveType(root); driveType {
	case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE, windows.DRIVE_RAMDISK:
		return nil
	case windows.DRIVE_REMOTE:
		return errors.New("state directory must not use a mapped network drive")
	default:
		return fmt.Errorf("state directory drive type is not safely local (%d)", driveType)
	}
}

func finalWindowsPath(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if length == 0 || length >= uint32(len(buffer)) {
		return "", errors.New("resolved state directory path is too long")
	}
	resolved := windows.UTF16ToString(buffer[:length])
	if strings.HasPrefix(strings.ToUpper(resolved), `\\?\UNC\`) {
		return `\\` + resolved[len(`\\?\UNC\`):], nil
	}
	if strings.HasPrefix(resolved, `\\?\`) {
		resolved = resolved[len(`\\?\`):]
	}
	return resolved, nil
}

func windowsNetworkPath(path string) bool {
	upper := strings.ToUpper(filepath.Clean(path))
	return strings.HasPrefix(upper, `\\`) || strings.HasPrefix(upper, `//`) || strings.HasPrefix(upper, `\\?\UNC\`)
}

func ProtectPrivatePath(path string, directory bool) error {
	user, err := processUser()
	if err != nil {
		return err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("build owner-only ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		return fmt.Errorf("set owner-only ACL: %w", err)
	}
	return validateOwnerOnly(path, user)
}

func ValidatePrivateFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("private input must be a regular file")
	}
	user, err := processUser()
	if err != nil {
		return err
	}
	return validateOwnerOnly(path, user)
}

func processUser() (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read current Windows user: %w", err)
	}
	return user.User.Sid, nil
}

func validateOwnerOnly(path string, user *windows.SID) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read private-file ACL: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(user) {
		return errors.New("private file must be owned by the current Windows user")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("private file ACL must not inherit access entries")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return errors.New("private file ACL must grant access only to its owner")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil {
		return errors.New("private file ACL cannot be inspected")
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !aceSID.Equals(user) || ace.Mask&windows.GENERIC_ALL != windows.GENERIC_ALL {
		return errors.New("private file ACL must grant full access only to its owner")
	}
	return nil
}
