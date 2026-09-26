//go:build linux

package service

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// Probe reads the environment of this process.
func Probe() Environment {
	return Environment{
		Getenv: os.Getenv, EUID: os.Geteuid(), Linux: true,
		InContainer: inContainer(), GraphicalSession: graphicalSession("/run/systemd/sessions"),
	}
}

// inContainer recognizes systemd's container marker, Docker and Podman
// marker files, and the container variable of process 1, which LXC sets
// and root can read.
func inContainer() bool {
	if content, err := os.ReadFile("/run/systemd/container"); err == nil && len(bytes.TrimSpace(content)) > 0 {
		return true
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	environment, err := os.ReadFile("/proc/1/environ")
	if err != nil {
		return false
	}
	for _, entry := range bytes.Split(environment, []byte{0}) {
		if value, found := bytes.CutPrefix(entry, []byte("container=")); found && len(value) > 0 {
			return true
		}
	}
	return false
}

// graphicalSession reports whether systemd-logind lists an X11 or Wayland
// session in its runtime directory, a user's or a login screen's.
func graphicalSession(directory string) bool {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		file, err := os.Open(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			switch strings.TrimSpace(scanner.Text()) {
			case "TYPE=x11", "TYPE=wayland", "TYPE=mir":
				file.Close()
				return true
			}
		}
		file.Close()
	}
	return false
}
