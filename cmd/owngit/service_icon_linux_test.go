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
	"owngit/internal/state"
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

	// A file that cannot be read may be someone else's: it stays too.
	if os.Geteuid() != 0 {
		noErr(t, os.WriteFile(path, []byte("[Desktop Entry]\nExec=unreadable\n"), 0o644))
		noErr(t, os.Chmod(path, 0))
		fixture.out.Reset()
		fixture.host.installIcon(service.ModeUser, stateDir, false)
		noErr(t, os.Chmod(path, 0o644))
		if kept, _ := os.ReadFile(path); string(kept) != "[Desktop Entry]\nExec=unreadable\n" || !strings.Contains(fixture.out.String(), path+" could not be read") || strings.Contains(fixture.out.String(), "when you sign in") {
			t.Fatalf("an unreadable entry: %q\n%s", kept, fixture.out.String())
		}
		noErr(t, os.Remove(path))
	}

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

// The install refuses, before anything changes, a service or icon entry
// that would run an owngit another account could replace; a binary in a
// folder of this account and Homebrew's opt link are fine.
func TestServiceInstallRunsOnlyAProtectedOwnGit(t *testing.T) {
	root := t.TempDir()
	folder := func(path string, mode os.FileMode) string {
		noErr(t, os.MkdirAll(path, 0o700))
		noErr(t, os.Chmod(path, mode))
		return path
	}
	binary := func(path string) string {
		noErr(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
		return path
	}
	own := binary(filepath.Join(folder(filepath.Join(root, "own"), 0o755), "owngit"))
	shared := binary(filepath.Join(folder(filepath.Join(root, "shared"), 0o777), "owngit"))
	linked := filepath.Join(folder(filepath.Join(root, "linked"), 0o755), "owngit")
	noErr(t, os.Symlink(shared, linked))
	cellar := filepath.Join(root, "brew", "Cellar", "owngit", "1.1.3", "bin")
	folder(cellar, 0o755)
	brewed := binary(filepath.Join(cellar, "owngit"))
	folder(filepath.Join(root, "brew", "opt"), 0o755)
	noErr(t, os.Symlink(filepath.Join("..", "Cellar", "owngit", "1.1.3"), filepath.Join(root, "brew", "opt", "owngit")))
	sharedCellar := filepath.Join(root, "sharedbrew", "Cellar", "owngit", "1.1.3", "bin")
	folder(sharedCellar, 0o755)
	sharedBrewed := binary(filepath.Join(sharedCellar, "owngit"))
	folder(filepath.Join(root, "sharedbrew", "opt"), 0o777)
	noErr(t, os.Symlink(filepath.Join("..", "Cellar", "owngit", "1.1.3"), filepath.Join(root, "sharedbrew", "opt", "owngit")))

	for _, test := range []struct {
		name, executable string
		mode             service.Mode
		refused          string
	}{
		{"a binary in a folder of this account", own, service.ModeUser, ""},
		{"a folder every account can write", shared, service.ModeUser, shared},
		{"a link into such a folder", linked, service.ModeSystem, linked},
		{"Homebrew's opt link", brewed, service.ModeHomebrew, ""},
		{"an opt link others can replace", sharedBrewed, service.ModeHomebrew, filepath.Join(root, "sharedbrew", "opt", "owngit", "bin", "owngit")},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallFixture(t, desktopEnv, nil, false)
			requireProtectedPath = state.RequireProtectedPath
			fixture.host.executable = test.executable
			err := fixture.host.requireProtectedExecutables(test.mode, false)
			if test.refused == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "the service would run "+test.refused+", but") || !strings.Contains(err.Error(), "~/.local/bin") {
				t.Fatalf("accepted or unclear: %v", err)
			}
			// The whole install stops before a unit, root script or icon
			// entry is written.
			if test.mode == service.ModeHomebrew {
				return
			}
			installErr := fixture.host.install(filepath.Join(t.TempDir(), "state"), nil)
			if installErr == nil || len(fixture.commands) != 0 || len(fixture.scripts) != 0 {
				t.Fatalf("install %v, commands %q, scripts %d", installErr, fixture.commands, len(fixture.scripts))
			}
			if _, err := os.Stat(service.UserUnitPath(fixture.host.userConfigDir)); !os.IsNotExist(err) {
				t.Fatalf("a unit was written: %v", err)
			}
			if _, err := os.Stat(service.AutostartPath(fixture.host.userConfigDir)); !os.IsNotExist(err) {
				t.Fatalf("an icon entry was written: %v", err)
			}
		})
	}
}
