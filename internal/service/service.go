// Package service installs OwnGit as a background service that starts at
// boot. It chooses who runs the service from the environment, without
// asking: a desktop user, the user who installs it over SSH, or a dedicated
// account when root installs it. Linux uses systemd. Other platforms plug
// in later through the same modes and commands.
package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Mode is who runs the service and which service manager starts it.
type Mode string

const (
	// ModeUser is a systemd user unit of the installing user. Lingering
	// starts that user's service manager at boot, without a login.
	ModeUser Mode = "user"
	// ModeSystem is a system unit that runs as the installing user and keeps
	// that user's state directory. Installing it needs root once.
	ModeSystem Mode = "system"
	// ModeAccount is a system unit that runs as the dedicated account
	// AccountName, with its state in AccountStateDir. Root installs it.
	ModeAccount Mode = "account"
	// ModeHomebrew leaves the service to "brew services", because Homebrew
	// installed the binary and upgrades it.
	ModeHomebrew Mode = "homebrew"
)

// Describe says in a few words who runs the service.
func (mode Mode) Describe() string {
	switch mode {
	case ModeUser:
		return "systemd user service of this user, started at boot"
	case ModeSystem:
		return "systemd system service that runs as the installing user"
	case ModeAccount:
		return "systemd system service that runs as the " + AccountName + " account"
	case ModeHomebrew:
		return "Homebrew service (brew services)"
	}
	return string(mode)
}

// Paths and names the system modes use.
const (
	UnitName        = "owngit.service"
	SystemUnitPath  = "/etc/systemd/system/" + UnitName
	AccountName     = "owngit"
	AccountHome     = "/var/lib/owngit"
	AccountStateDir = AccountHome + "/state"
	// PointerPath names the state directory of the ModeAccount service, so
	// that root and members of the owngit group find it without options.
	PointerPath = "/etc/owngit/state-dir"
)

// ErrUnsupported is returned on a platform without a service backend yet.
var ErrUnsupported = errors.New("not supported on this platform yet")

// Environment is what mode selection and headless detection look at.
type Environment struct {
	// Getenv reads an environment variable.
	Getenv func(string) string
	// EUID is the effective user ID; 0 is root.
	EUID int
	// Linux is true on Linux, where the systemd modes apply.
	Linux bool
	// InContainer is true inside a container or LXC.
	InContainer bool
	// GraphicalSession is true when systemd-logind lists a graphical
	// session (X11 or Wayland, including a login screen), as a desktop or
	// laptop in use has. Servers and containers have none. A seat that
	// could show graphics is not enough: every virtual machine with a
	// virtual display adapter has one.
	GraphicalSession bool
}

// Headless reports whether the owner is likely not sitting at this
// computer, so a browser on this computer cannot finish setup. It is true
// on Linux when any of these holds:
//   - root runs inside a container or LXC;
//   - an SSH session without a display (neither DISPLAY nor WAYLAND_DISPLAY);
//   - no graphical session exists on this computer, and no display is set
//     outside SSH (WSLg and desktops without logind set only a display).
//
// Other platforms are never headless here yet.
func (env Environment) Headless() bool {
	if !env.Linux {
		return false
	}
	if env.EUID == 0 && env.InContainer {
		return true
	}
	display := env.Getenv("DISPLAY") != "" || env.Getenv("WAYLAND_DISPLAY") != ""
	ssh := env.Getenv("SSH_CONNECTION") != "" || env.Getenv("SSH_CLIENT") != "" || env.Getenv("SSH_TTY") != ""
	if ssh && !display {
		return true
	}
	return !env.GraphicalSession && !(display && !ssh)
}

// ChooseMode picks the mode for a new installation: Homebrew when it
// installed the binary, the dedicated account for root, a user service on
// a desktop, and a system service running as the installing user
// otherwise. A user service that cannot start at boot falls back to
// ModeSystem when it is installed.
func (env Environment) ChooseMode(homebrew bool) Mode {
	switch {
	case homebrew && env.EUID != 0:
		return ModeHomebrew
	case env.EUID == 0:
		return ModeAccount
	case env.Headless():
		return ModeSystem
	default:
		return ModeUser
	}
}

// HomebrewPrefix returns the Homebrew prefix when executable (with symbolic
// links resolved) lies in Homebrew's Cellar for the owngit formula, and ""
// otherwise.
func HomebrewPrefix(executable string) string {
	slashed := filepath.ToSlash(executable)
	prefix, _, found := strings.Cut(slashed, "/Cellar/owngit/")
	if !found || prefix == "" {
		return ""
	}
	return filepath.FromSlash(prefix)
}

// checkAbsolute refuses a relative or unclean path, so every unit names the
// same file wherever systemd starts it.
func checkAbsolute(label, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%s must be an absolute clean path: %q", label, path)
	}
	return nil
}
