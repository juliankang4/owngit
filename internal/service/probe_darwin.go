//go:build darwin

package service

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// Probe reads the environment of this process.
func Probe() Environment {
	return Environment{
		Getenv: os.Getenv, EUID: os.Geteuid(), Darwin: true,
		GraphicalSession: GUIDomainExists(DesktopUID(os.Getuid(), consoleOwner())),
	}
}

// GUIDomainExists reports whether launchd has the GUI domain of uid, which
// exists while that user is logged in at the desktop, in front or in the
// background of fast user switching. A LaunchAgent can load there only
// then.
func GUIDomainExists(uid int) bool {
	return exec.Command(launchctl, "print", "gui/"+strconv.Itoa(uid)).Run() == nil
}

// consoleOwner returns the owner of /dev/console: the user logged in on the
// screen, or root while the login window shows. It returns -1 when the
// owner cannot be read.
func consoleOwner() int {
	info, err := os.Stat("/dev/console")
	if err != nil {
		return -1
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid)
	}
	return -1
}
