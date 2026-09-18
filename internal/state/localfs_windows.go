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

// Windows maps an effective GENERIC_ALL file ACE to this object-specific mask.
// Using it directly keeps the stored ACL canonical while inherited grants may
// still retain GENERIC_ALL for child objects.
const fileAllAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

func ProtectPrivatePath(path string, directory bool) error {
	user, defaultOwner, err := processIdentity()
	if err != nil {
		return err
	}
	if err := validateProcessOwned(path, user, defaultOwner); err != nil {
		return err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: fileAllAccess,
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
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user, nil, acl, nil); err != nil {
		return fmt.Errorf("set owner-only ACL: %w", err)
	}
	return validateOwnerOnly(path, user, directory)
}

func ValidatePrivateFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("private input must be a regular file")
	}
	user, _, err := processIdentity()
	if err != nil {
		return err
	}
	return validateOwnerOnly(path, user, false)
}

type tokenOwner struct {
	Owner *windows.SID
}

func processIdentity() (*windows.SID, *windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return nil, nil, fmt.Errorf("read current Windows user: %w", err)
	}
	user, err := tokenUser.User.Sid.Copy()
	if err != nil {
		return nil, nil, fmt.Errorf("copy current Windows user: %w", err)
	}
	var size uint32
	err = windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &size)
	if err != windows.ERROR_INSUFFICIENT_BUFFER || size == 0 {
		return nil, nil, fmt.Errorf("read current Windows token owner size: %w", err)
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenOwner, &buffer[0], size, &size); err != nil {
		return nil, nil, fmt.Errorf("read current Windows token owner: %w", err)
	}
	owner := (*tokenOwner)(unsafe.Pointer(&buffer[0])).Owner
	if owner == nil {
		return nil, nil, errors.New("current Windows token has no default owner")
	}
	defaultOwner, err := owner.Copy()
	if err != nil {
		return nil, nil, fmt.Errorf("copy current Windows token owner: %w", err)
	}
	return user, defaultOwner, nil
}

func validateProcessOwned(path string, user, defaultOwner *windows.SID) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read private-file owner: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || !ownerMatchesProcess(owner, user, defaultOwner) {
		return errors.New("private file must be owned by the current Windows user or its token owner")
	}
	return nil
}

func ownerMatchesProcess(owner, user, defaultOwner *windows.SID) bool {
	return owner != nil && (owner.Equals(user) || owner.Equals(defaultOwner))
}

func validateOwnerOnly(path string, user *windows.SID, directory bool) error {
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
	if err != nil || dacl == nil || dacl.AceCount == 0 {
		return errors.New("private file ACL must grant access only to its owner")
	}

	// Windows can split a directory grant into an effective FILE_ALL_ACCESS ACE
	// and an inherit-only GENERIC_ALL ACE. Validate their security semantics
	// rather than requiring a platform-dependent ACE count.
	effectiveFullAccess := false
	objectsInherit := false
	containersInherit := false
	allowedFlags := uint32(0)
	if directory {
		allowedFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE | windows.INHERIT_ONLY_ACE
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil || ace == nil {
			return errors.New("private file ACL cannot be inspected")
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("private file ACL must grant access only to its owner")
		}
		flags := uint32(ace.Header.AceFlags)
		if flags & ^allowedFlags != 0 || flags&windows.INHERIT_ONLY_ACE != 0 && flags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) == 0 {
			return errors.New("private file ACL has unsafe inheritance flags")
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !aceSID.Equals(user) || !hasFullFileAccess(ace.Mask) {
			return errors.New("private file ACL must grant full access only to its owner")
		}
		if flags&windows.INHERIT_ONLY_ACE == 0 {
			effectiveFullAccess = true
		}
		objectsInherit = objectsInherit || flags&windows.OBJECT_INHERIT_ACE != 0
		containersInherit = containersInherit || flags&windows.CONTAINER_INHERIT_ACE != 0
	}
	if !effectiveFullAccess {
		return errors.New("private file ACL must include an effective owner grant")
	}
	if directory && (!objectsInherit || !containersInherit) {
		return errors.New("private directory ACL must protect inherited files and directories")
	}
	return nil
}

func hasFullFileAccess(mask windows.ACCESS_MASK) bool {
	return mask&windows.GENERIC_ALL == windows.GENERIC_ALL || mask&fileAllAccess == fileAllAccess
}
