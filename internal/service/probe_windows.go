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
	elevated := token.IsElevated()
	return Environment{
		Getenv: os.Getenv, EUID: os.Geteuid(), Windows: true,
		NoDesktop: !visibleDesktop(), Elevated: elevated,
		Administrator: elevated || limitedAdministrator(token),
	}
}

// limitedAdministrator reports whether the token is the filtered half of an
// administrator's pair (UAC's "limited" elevation type), or belongs to the
// Administrators group without UAC.
func limitedAdministrator(token windows.Token) bool {
	var elevationType uint32
	var size uint32
	if err := windows.GetTokenInformation(token, windows.TokenElevationType, (*byte)(unsafe.Pointer(&elevationType)), uint32(unsafe.Sizeof(elevationType)), &size); err == nil && elevationType == 3 {
		return true
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	member, err := windows.Token(0).IsMember(administrators)
	return err == nil && member
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
