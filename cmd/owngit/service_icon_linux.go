//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"

	"owngit/internal/service"
	"owngit/internal/tray"
)

// On a Linux desktop "owngit service install" also makes the OwnGit icon
// start when the account signs in, through its XDG autostart entry, and
// starts it now when this terminal is on that desktop. The server never
// depends on the icon: every icon step only reports what it could not do.
// A headless install has no icon and removes the entry of an earlier one;
// the dedicated owngit account, which no one signs in as, never gets one.

// installIcon registers the icon of the server of stateDir that mode runs,
// or removes it for a headless install.
func (host *serviceHost) installIcon(mode service.Mode, stateDir string, headless bool) {
	if mode == service.ModeAccount {
		// Nobody signs in to a desktop as the dedicated account.
		return
	}
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
	// The entry outlives this binary: a Homebrew upgrade removes its
	// versioned Cellar folder, so the entry names the opt link instead.
	entry, err := service.RenderAutostart(service.AgentExecutable("", host.executable), stateDir)
	if err == nil {
		err = service.WriteAutostart(path, entry)
	}
	if err != nil {
		host.printf("The OwnGit icon does not start at sign-in: %v.\n", err)
		return
	}
	reloadAutostart()
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
		reloadAutostart()
		if err == nil && said {
			host.printf("The OwnGit icon no longer starts at sign-in. An icon that shows now stays until you choose Quit the icon in its menu or sign out.\n")
		}
	}
	if err != nil {
		host.printf("The OwnGit icon's autostart entry %s stays: %v.\n", path, err)
	}
}

// reloadAutostart has the account's systemd read the autostart folder
// again. Desktops that start autostart entries through systemd read it when
// systemd starts for the account, which with a user service that starts at
// boot happens once, not at every sign-in. Without systemd for the account
// the desktop reads the folder itself, so a failure changes nothing.
func reloadAutostart() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := service.UserSystemctl("daemon-reload")
	_, _ = serviceRunner(ctx, command[0], command[1:]...)
}

// startIconNow starts "owngit tray icon" in its own session, so it
// outlives this command. An icon that already runs for the state
// directory ends the new one at once.
var startIconNow = func(executable, stateDir string) {
	command := exec.Command(executable, "tray", "icon", "--state-dir", stateDir)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if command.Start() == nil {
		_ = command.Process.Release()
	}
}
