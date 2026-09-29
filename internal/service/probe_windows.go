//go:build windows

package service

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procGetProcessWindowStation  = user32.NewProc("GetProcessWindowStation")
	procGetUserObjectInformation = user32.NewProc("GetUserObjectInformationW")
)

// Probe reads the environment of this process.
func Probe() Environment {
	token := windows.GetCurrentProcessToken()
	return Environment{
		Getenv: os.Getenv, EUID: os.Geteuid(), Windows: true,
		NoDesktop: !visibleDesktop(), Elevated: token.IsElevated(),
		Administrator: AdministratorAccount(token),
	}
}

// AdministratorAccount reports whether the account of token belongs to the
// Administrators group, whatever its rights now: the group is enabled in an
// elevated token, and deny-only in UAC's limited token and in a copy that
// OwnGit runs without administrator rights. A standard account's token
// does not list it.
func AdministratorAccount(token windows.Token) bool {
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return false
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.Equals(administrators) {
			return true
		}
	}
	return false
}

// visibleDesktop reports whether this process runs in a signed-in
// session on a window station that a person sees. Services, scheduled
// tasks without a sign-in and SSH sessions run in session 0.
func visibleDesktop() bool {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil || session == 0 {
		return false
	}
	station, _, _ := procGetProcessWindowStation.Call()
	if station == 0 {
		return false
	}
	// USEROBJECTFLAGS; WSF_VISIBLE is bit 0 of dwFlags.
	var flags struct {
		inherit, reserved int32
		flags             uint32
	}
	var needed uint32
	const uoiFlags = 1
	ok, _, _ := procGetUserObjectInformation.Call(station, uoiFlags, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags), uintptr(unsafe.Pointer(&needed)))
	return ok != 0 && flags.flags&1 != 0
}
