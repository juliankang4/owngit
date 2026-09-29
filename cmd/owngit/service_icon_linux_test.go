//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"owngit/internal/service"
)

// On a Linux desktop the install makes the icon start at sign-in through
// the account's autostart entry and has systemd read the folder again; a
// headless install and the uninstall remove only OwnGit's own entry.
func TestServiceInstallRegistersTheIconAtSignIn(t *testing.T) {
	fixture := newInstallFixture(t, sshEnv, nil, false)
	bin := t.TempDir()
	noErr(t, os.WriteFile(filepath.Join(bin, "gjs"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", bin)
	stateDir := filepath.Join(t.TempDir(), "state")
	path := service.AutostartPath(fixture.host.userConfigDir)

	fixture.host.installIcon(service.ModeUser, stateDir, false)
	entry, err := os.ReadFile(path)
	noErr(t, err)
	if !strings.Contains(string(entry), `Exec="/usr/local/bin/owngit" "tray" "icon" "--state-dir" "`+stateDir+`"`) ||
		!strings.Contains(fixture.out.String(), "when you sign in") || !slices.Contains(fixture.commands, "systemctl --user daemon-reload") {
		t.Fatalf("entry\n%s\noutput\n%s\ncommands %q", entry, fixture.out.String(), fixture.commands)
	}

	fixture.host.installIcon(service.ModeUser, stateDir, true)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a headless install kept the icon's entry: %v", err)
	}

	noErr(t, os.WriteFile(path, []byte("[Desktop Entry]\nExec=someone-elses\n"), 0o644))
	fixture.out.Reset()
	fixture.host.installIcon(service.ModeUser, stateDir, false)
	fixture.host.removeIcon(true)
	if kept, _ := os.ReadFile(path); string(kept) != "[Desktop Entry]\nExec=someone-elses\n" || !strings.Contains(fixture.out.String(), "was not written by OwnGit") {
		t.Fatalf("a foreign entry: %q\n%s", kept, fixture.out.String())
	}
	noErr(t, os.Remove(path))

	fixture.host.installIcon(service.ModeUser, stateDir, false)
	fixture.out.Reset()
	fixture.host.removeIcon(true)
	if _, err := os.Stat(path); !os.IsNotExist(err) || !strings.Contains(fixture.out.String(), "no longer starts at sign-in") {
		t.Fatalf("uninstall kept the entry (%v):\n%s", err, fixture.out.String())
	}

	t.Setenv("PATH", t.TempDir())
	fixture.out.Reset()
	fixture.host.installIcon(service.ModeUser, stateDir, false)
	if _, err := os.Stat(path); !os.IsNotExist(err) || !strings.Contains(fixture.out.String(), "needs gjs with GTK 4") {
		t.Fatalf("without gjs (%v):\n%s", err, fixture.out.String())
	}

	shared := t.TempDir()
	noErr(t, os.WriteFile(filepath.Join(shared, "gjs"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	noErr(t, os.Chmod(shared, 0o777))
	t.Setenv("PATH", shared)
	fixture.out.Reset()
	fixture.host.installIcon(service.ModeUser, stateDir, false)
	if _, err := os.Stat(path); !os.IsNotExist(err) || !strings.Contains(fixture.out.String(), "does not start gjs at "+filepath.Join(shared, "gjs")) {
		t.Fatalf("a gjs others can replace (%v):\n%s", err, fixture.out.String())
	}
}

// The entry names Homebrew's opt link, which an upgrade keeps, not the
// versioned Cellar folder the command runs from; the dedicated account
// never gets an entry.
func TestServiceIconEntryPathAndAccount(t *testing.T) {
	fixture := newInstallFixture(t, sshEnv, nil, false)
	bin := t.TempDir()
	noErr(t, os.WriteFile(filepath.Join(bin, "gjs"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", bin)
	stateDir := filepath.Join(t.TempDir(), "state")
	path := service.AutostartPath(fixture.host.userConfigDir)

	fixture.host.executable = "/home/linuxbrew/.linuxbrew/Cellar/owngit/1.1.3/bin/owngit"
	fixture.host.installIcon(service.ModeHomebrew, stateDir, false)
	entry, err := os.ReadFile(path)
	noErr(t, err)
	if !strings.Contains(string(entry), `Exec="/home/linuxbrew/.linuxbrew/opt/owngit/bin/owngit" "tray" "icon"`) {
		t.Fatalf("Homebrew entry:\n%s", entry)
	}
	noErr(t, os.Remove(path))

	fixture.out.Reset()
	fixture.host.installIcon(service.ModeAccount, stateDir, false)
	if _, err := os.Stat(path); !os.IsNotExist(err) || fixture.out.Len() != 0 {
		t.Fatalf("the dedicated account got an entry (%v):\n%s", err, fixture.out.String())
	}
}

// "owngit service install" on a desktop registers the icon once the
// service answers, for a user service and for a system service that runs
// as the user.
func TestServiceInstallOnADesktopStartsTheIcon(t *testing.T) {
	bin := t.TempDir()
	noErr(t, os.WriteFile(filepath.Join(bin, "gjs"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", bin)
	for _, managerDown := range []bool{false, true} {
		fixture := newInstallFixture(t, desktopEnv, nil, managerDown)
		lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
		waitForService = func(string, time.Duration) (string, error) { return "127.0.0.1:7654", nil }
		var started []string
		previous := startIconNow
		startIconNow = func(executable, stateDir string) { started = append(started, executable, stateDir) }
		stateDir := filepath.Join(t.TempDir(), "state")
		err := fixture.host.install(stateDir, nil)
		startIconNow = previous
		entry, readErr := os.ReadFile(service.AutostartPath(fixture.host.userConfigDir))
		if err != nil || readErr != nil || !strings.Contains(string(entry), `"--state-dir" "`+stateDir+`"`) ||
			!slices.Equal(started, []string{"/usr/local/bin/owngit", stateDir}) {
			t.Fatalf("manager down %v: install %v, entry %v %q, started %q\n%s", managerDown, err, readErr, entry, started, fixture.out.String())
		}
	}
}
