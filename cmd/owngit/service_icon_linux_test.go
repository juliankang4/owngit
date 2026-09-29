//go:build linux

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

	fixture.host.installIcon(stateDir, false)
	entry, err := os.ReadFile(path)
	noErr(t, err)
	if !strings.Contains(string(entry), `Exec="/usr/local/bin/owngit" "tray" "icon" "--state-dir" "`+stateDir+`"`) ||
		!strings.Contains(fixture.out.String(), "when you sign in") || !slices.Contains(fixture.commands, "systemctl --user daemon-reload") {
		t.Fatalf("entry\n%s\noutput\n%s\ncommands %q", entry, fixture.out.String(), fixture.commands)
	}

	fixture.host.installIcon(stateDir, true)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a headless install kept the icon's entry: %v", err)
	}

	noErr(t, os.WriteFile(path, []byte("[Desktop Entry]\nExec=someone-elses\n"), 0o644))
	fixture.out.Reset()
	fixture.host.installIcon(stateDir, false)
	fixture.host.removeIcon(true)
	if kept, _ := os.ReadFile(path); string(kept) != "[Desktop Entry]\nExec=someone-elses\n" || !strings.Contains(fixture.out.String(), "was not written by OwnGit") {
		t.Fatalf("a foreign entry: %q\n%s", kept, fixture.out.String())
	}
	noErr(t, os.Remove(path))

	fixture.host.installIcon(stateDir, false)
	fixture.out.Reset()
	fixture.host.removeIcon(true)
	if _, err := os.Stat(path); !os.IsNotExist(err) || !strings.Contains(fixture.out.String(), "no longer starts at sign-in") {
		t.Fatalf("uninstall kept the entry (%v):\n%s", err, fixture.out.String())
	}

	t.Setenv("PATH", t.TempDir())
	fixture.out.Reset()
	fixture.host.installIcon(stateDir, false)
	if _, err := os.Stat(path); !os.IsNotExist(err) || !strings.Contains(fixture.out.String(), "needs gjs with GTK 4") {
		t.Fatalf("without gjs (%v):\n%s", err, fixture.out.String())
	}
}
