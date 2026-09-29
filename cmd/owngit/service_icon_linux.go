//go:build linux

package main

import (
	"os"
	"os/exec"
	"syscall"

	"owngit/internal/service"
	"owngit/internal/tray"
)

// On a Linux desktop "owngit service install" also makes the OwnGit icon
// start when the account signs in, through its XDG autostart entry, and
// starts it now when this terminal is on that desktop. The server never
// depends on the icon: every icon step only reports what it could not do.
// A headless install has no icon and removes the entry of an earlier one;
// the dedicated owngit account, which no one signs in as, never gets one.

// installIcon registers the icon of the server of stateDir, or removes it
// for a headless install.
func (host *serviceHost) installIcon(stateDir string, headless bool) {
	if headless || !host.env.Desktop() {
		host.removeIcon(false)
		return
	}
	path := service.AutostartPath(host.userConfigDir)
	if problem := tray.IconProblem(); problem != "" {
		host.printf("The OwnGit icon does not start at sign-in: %s.\n", problem)
		return
	}
	found, err := service.ReadAutostart(path)
	if err == nil && found == service.AutostartForeign {
		host.printf("The OwnGit icon does not start at sign-in: %s was not written by OwnGit, so it stays.\n", path)
		return
	}
	entry, err := service.RenderAutostart(host.executable, stateDir)
	if err == nil {
		err = service.WriteAutostart(path, entry)
	}
	if err != nil {
		host.printf("The OwnGit icon does not start at sign-in: %v.\n", err)
		return
	}
	if !host.env.Headless() {
		startIconNow(host.executable, stateDir)
	}
	host.printf("The OwnGit icon shows in the desktop's panel when you sign in (%s), and now if this terminal is on that desktop. \"owngit tray off\" hides it; OwnGit keeps running without it.\n", path)
}

// removeIcon removes the icon's autostart entry that OwnGit wrote. said
// asks for a line when there was one.
func (host *serviceHost) removeIcon(said bool) {
	path := service.AutostartPath(host.userConfigDir)
	found, err := service.ReadAutostart(path)
	if err == nil && found == service.AutostartOwn {
		err = os.Remove(path)
		if err == nil && said {
			host.printf("The OwnGit icon no longer starts at sign-in. An icon that shows now stays until you choose Quit the icon in its menu or sign out.\n")
		}
	}
	if err != nil {
		host.printf("The OwnGit icon's autostart entry %s stays: %v.\n", path, err)
	}
}

// startIconNow starts "owngit tray icon" in its own session, so it
// outlives this command. An icon that already runs for the state
// directory ends the new one at once.
func startIconNow(executable, stateDir string) {
	command := exec.Command(executable, "tray", "icon", "--state-dir", stateDir)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if command.Start() == nil {
		_ = command.Process.Release()
	}
}
