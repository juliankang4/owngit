//go:build windows

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// CreatePrivateFile creates a new file with its final owner-only descriptor and
// keeps delete sharing disabled while the returned handle is open.
func CreatePrivateFile(path string) (*os.File, error) {
	user, _, err := processIdentity()
	if err != nil {
		return nil, err
	}
	descriptor, err := ownerOnlySecurityDescriptor(user)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	access := uint32(windows.GENERIC_WRITE | windows.READ_CONTROL | windows.WRITE_DAC | windows.WRITE_OWNER)
	handle, err := windows.CreateFile(name, access, 0, attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: errors.New("create private file handle")}
	}
	return file, nil
}

func ownerOnlySecurityDescriptor(user *windows.SID) (*windows.SECURITY_DESCRIPTOR, error) {
	acl, err := ownerOnlyACL(user, false)
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil, fmt.Errorf("create owner-only security descriptor: %w", err)
	}
	if err := descriptor.SetOwner(user, false); err != nil {
		return nil, fmt.Errorf("set private-file owner: %w", err)
	}
	if err := descriptor.SetDACL(acl, true, false); err != nil {
		return nil, fmt.Errorf("set private-file ACL: %w", err)
	}
	if err := descriptor.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return nil, fmt.Errorf("protect private-file ACL: %w", err)
	}
	relative, err := descriptor.ToSelfRelative()
	if err != nil {
		return nil, fmt.Errorf("encode owner-only security descriptor: %w", err)
	}
	return relative, nil
}

func ownerOnlyACL(user *windows.SID, directory bool) (*windows.ACL, error) {
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
		return nil, fmt.Errorf("build owner-only ACL: %w", err)
	}
	return acl, nil
}

func ProtectPrivatePath(path string, directory bool) error {
	user, defaultOwner, err := processIdentity()
	if err != nil {
		return err
	}
	if err := validateProcessOwned(path, user, defaultOwner); err != nil {
		return err
	}
	acl, err := ownerOnlyACL(user, directory)
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user, nil, acl, nil); err != nil {
		return fmt.Errorf("set owner-only ACL: %w", err)
	}
	return validateOwnerOnly(path, user, directory)
}

// ProtectPrivateHandle applies and verifies the owner-only ACL through the held
// handle, so a pathname replacement cannot change what was protected.
func ProtectPrivateHandle(file *os.File, directory bool) error {
	user, _, err := processIdentity()
	if err != nil {
		return err
	}
	acl, err := ownerOnlyACL(user, directory)
	if err != nil {
		return err
	}
	handle := windows.Handle(file.Fd())
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user, nil, acl, nil); err != nil {
		return fmt.Errorf("set owner-only ACL on handle: %w", err)
	}
	return validateOwnerOnlyHandle(handle, user, directory)
}

func validateOwnerOnlyHandle(handle windows.Handle, user *windows.SID, directory bool) error {
	descriptor, err := handleDescriptor(handle)
	if err != nil {
		return err
	}
	return validateOwnerOnlyDescriptor(descriptor, user, directory)
}

func handleDescriptor(handle windows.Handle) (*windows.SECURITY_DESCRIPTOR, error) {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("read private-handle ACL: %w", err)
	}
	return descriptor, nil
}

// ValidatePrivateFile checks a file that OwnGit wrote and protected itself,
// such as a lock or marker file: it must be a regular file owned by the
// current user and accessible only to that user.
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

// ValidatePrivateFileHandle is ValidatePrivateFile for an open file, without
// reopening its path.
func ValidatePrivateFileHandle(file *os.File) error {
	info, err := file.Stat()
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
	return validateOwnerOnlyHandle(windows.Handle(file.Fd()), user, false)
}

// ValidatePrivateInputFile checks a secret file that the owner may have made
// by hand, such as a password or token file. Access must be limited to the
// current user as for ValidatePrivateFile, but the owner may also be the
// Administrators group (see validatePrivateInputDescriptor). A refusal is a
// *NotPrivateError that explains it.
func ValidatePrivateInputFile(path string) error {
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
	descriptor, err := pathDescriptor(path)
	if err != nil {
		return err
	}
	return validatePrivateInput(descriptor, user, path)
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
	if descriptor == nil {
		return errors.New("private file has no security descriptor")
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
	descriptor, err := pathDescriptor(path)
	if err != nil {
		return err
	}
	return validateOwnerOnlyDescriptor(descriptor, user, directory)
}

func pathDescriptor(path string) (*windows.SECURITY_DESCRIPTOR, error) {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("read private-file ACL: %w", err)
	}
	return descriptor, nil
}

// validateOwnerOnlyDescriptor checks a path OwnGit protected itself, so the
// owner must be exactly the current user it set.
func validateOwnerOnlyDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, user *windows.SID, directory bool) error {
	if descriptor == nil {
		return errors.New("private file has no security descriptor")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(user) {
		return errors.New("private file must be owned by the current Windows user")
	}
	return validateUserOnlyDACL(descriptor, user, directory)
}

// validatePrivateInputDescriptor checks a file OwnGit reads, which may have
// been made by hand. Its ACL must grant access only to the current user, as
// for files OwnGit protects. Its owner may be the current user or the
// Administrators group, which an elevated shell makes the owner of new files.
// Administrators can take ownership of any file anyway, so accepting them as
// owner does not widen who can read the file.
func validatePrivateInputDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, user *windows.SID) error {
	if descriptor == nil {
		return errors.New("private file has no security descriptor")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || !privateInputOwner(owner, user) {
		return errors.New("private file must be owned by the current Windows user or the Administrators group")
	}
	return validateUserOnlyDACL(descriptor, user, false)
}

// validatePrivateInput checks a file OwnGit reads and, when it is refused,
// explains why in a *NotPrivateError.
func validatePrivateInput(descriptor *windows.SECURITY_DESCRIPTOR, user *windows.SID, path string) error {
	refused := validatePrivateInputDescriptor(descriptor, user)
	if refused == nil || descriptor == nil {
		return refused
	}
	return describeNotPrivate(descriptor, user, path, refused)
}

// describeNotPrivate explains why validatePrivateInputDescriptor refused a
// file: an owner other than the current user or Administrators, inherited
// entries, other accounts that can access it, or a missing or partial grant
// to the current user. It also builds the PowerShell line that fixes it. It
// only reports; the decision stays with validatePrivateInputDescriptor.
func describeNotPrivate(descriptor *windows.SECURITY_DESCRIPTOR, user *windows.SID, path string, refused error) *NotPrivateError {
	var problems, fixes []string
	if owner, _, err := descriptor.Owner(); err != nil || !privateInputOwner(owner, user) {
		name := "unknown"
		if err == nil && owner != nil {
			name = accountName(owner)
		}
		problems = append(problems, "its owner is "+name+", not your account or Administrators")
		fixes = append(fixes, "icacls "+powerShellQuote(path)+" /setowner "+powerShellQuote("*"+user.String()))
	}
	if validateUserOnlyDACL(descriptor, user, false) != nil {
		problems = append(problems, describeACL(descriptor, user)...)
		fixes = append(fixes, userOnlyACLCommand(path, user))
	}
	if len(problems) == 0 {
		problems = append(problems, refused.Error())
	}
	return &NotPrivateError{Problem: strings.Join(problems, "; "), Fix: strings.Join(fixes, "; "), Shell: "PowerShell"}
}

// userOnlyACLCommand is a PowerShell line that replaces the whole access list
// of path with one full-control entry for user and no inherited entries
// (SDDL "D:P(A;;FA;;;SID)"). It writes the complete list instead of editing
// entries with icacls, because icacls can leave entries behind: /remove turns
// an inherited entry in a protected list into an explicit one instead of
// removing it.
//
// Set-Acl also writes the audit list, which needs a privilege an ordinary user
// does not have, unless the object's audit protection equals the file's
// current access protection (Set-Acl compares those two flags). So the line
// starts from Get-Acl and copies the file's access protection into the audit
// protection before it replaces the access list.
func userOnlyACLCommand(path string, user *windows.SID) string {
	quoted := powerShellQuote(path)
	return "$acl = Get-Acl -LiteralPath " + quoted + "; " +
		"$acl.SetAuditRuleProtection($acl.AreAccessRulesProtected, $true); " +
		"$acl.SetSecurityDescriptorSddlForm('D:P(A;;FA;;;" + user.String() + ")', 'Access'); " +
		"Set-Acl -LiteralPath " + quoted + " -AclObject $acl"
}

// powerShellQuote quotes s as a PowerShell single-quoted string, in which
// nothing is expanded, so a path with $, a backtick or $( ) stays text.
// PowerShell also ends such a string at the typographic single quotes, so
// every single quote character is doubled.
func powerShellQuote(s string) string {
	var quoted strings.Builder
	quoted.WriteByte('\'')
	for _, character := range s {
		switch character {
		case '\'', '\u2018', '\u2019', '\u201a', '\u201b':
			quoted.WriteRune(character)
		}
		quoted.WriteRune(character)
	}
	quoted.WriteByte('\'')
	return quoted.String()
}

// describeACL names what makes the file's access list more than one full
// grant to user.
func describeACL(descriptor *windows.SECURITY_DESCRIPTOR, user *windows.SID) []string {
	var problems, others, deniedOthers []string
	userDenied, userFull, userFlags, unknownEntries := false, false, false, false
	control, _, _ := descriptor.Control()
	if control&windows.SE_DACL_PROTECTED == 0 {
		problems = append(problems, "it inherits access entries from its folder")
	}
	dacl, _, err := descriptor.DACL()
	switch {
	case err != nil || dacl == nil:
		problems = append(problems, "it has no access list, so every account can access it")
	default:
		for index := uint16(0); index < dacl.AceCount; index++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(dacl, uint32(index), &ace); err != nil || ace == nil {
				unknownEntries = true
				continue
			}
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
				unknownEntries = true
				continue
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			inherited := ace.Header.AceFlags&windows.INHERITED_ACE != 0
			switch {
			case sid.Equals(user) && ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE:
				userDenied = true
			case sid.Equals(user) && !inherited && ace.Header.AceFlags != 0:
				userFlags = true
			case sid.Equals(user):
				if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 && hasFullFileAccess(ace.Mask) {
					userFull = true
				}
			case ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE:
				deniedOthers = appendNew(deniedOthers, accountName(sid))
			default:
				others = appendNew(others, accountName(sid))
			}
		}
	}
	if len(others) != 0 {
		problems = append(problems, strings.Join(others, ", ")+" can also access it")
	}
	if len(deniedOthers) != 0 {
		problems = append(problems, "its access list also has deny entries for "+strings.Join(deniedOthers, ", "))
	}
	if userDenied {
		problems = append(problems, "it denies your account some access")
	}
	if userFlags {
		problems = append(problems, "its entry for your account has inheritance flags meant for folders")
	}
	if !userFull {
		problems = append(problems, "your account does not have full control of it")
	}
	if unknownEntries {
		problems = append(problems, "its access list has entries OwnGit cannot check")
	}
	return problems
}

func appendNew(list []string, item string) []string {
	if slices.Contains(list, item) {
		return list
	}
	return append(list, item)
}

// accountName names sid as DOMAINccount, or by the SID string when it
// cannot be looked up.
func accountName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	switch {
	case err != nil || account == "":
		return sid.String()
	case domain == "":
		return account
	default:
		return domain + `\` + account
	}
}

func privateInputOwner(owner, user *windows.SID) bool {
	if owner == nil {
		return false
	}
	return owner.Equals(user) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

func validateUserOnlyDACL(descriptor *windows.SECURITY_DESCRIPTOR, user *windows.SID, directory bool) error {
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("private file ACL must not inherit access entries")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount == 0 {
		return errors.New("private file ACL must grant access only to the current Windows user")
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
			return errors.New("private file ACL must grant access only to the current Windows user")
		}
		flags := uint32(ace.Header.AceFlags)
		if flags & ^allowedFlags != 0 || flags&windows.INHERIT_ONLY_ACE != 0 && flags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) == 0 {
			return errors.New("private file ACL has unsafe inheritance flags")
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !aceSID.Equals(user) || !hasFullFileAccess(ace.Mask) {
			return errors.New("private file ACL must grant full access only to the current Windows user")
		}
		if flags&windows.INHERIT_ONLY_ACE == 0 {
			effectiveFullAccess = true
		}
		objectsInherit = objectsInherit || flags&windows.OBJECT_INHERIT_ACE != 0
		containersInherit = containersInherit || flags&windows.CONTAINER_INHERIT_ACE != 0
	}
	if !effectiveFullAccess {
		return errors.New("private file ACL must include an effective grant to the current Windows user")
	}
	if directory && (!objectsInherit || !containersInherit) {
		return errors.New("private directory ACL must protect inherited files and directories")
	}
	return nil
}

func hasFullFileAccess(mask windows.ACCESS_MASK) bool {
	return mask&windows.GENERIC_ALL == windows.GENERIC_ALL || mask&fileAllAccess == fileAllAccess
}
