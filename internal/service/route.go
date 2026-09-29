package service

import (
	"path/filepath"
	"strings"
)

// Route is who put the OwnGit program on this computer. The one who put a
// file there updates and removes it: a package manager owns the files it
// installed, a person owns a release archive they unpacked, and OwnGit owns
// only what "owngit service install" created (the service registration
// and, on Windows, the protected copy in Program Files).
type Route string

const (
	// RouteArchive is a release archive unpacked by hand, or a build from
	// source: no package manager owns the file.
	RouteArchive Route = "archive"
	// RouteHomebrew is a Homebrew install; the file lies in the Cellar.
	RouteHomebrew Route = "homebrew"
	// RouteNPM is the executable of an npm platform package.
	RouteNPM Route = "npm"
	// RoutePacman is a file that pacman lists as part of an installed
	// package, such as owngit-bin.
	RoutePacman Route = "pacman"
	// RouteApp is the helper inside an OwnGit.app bundle, which is replaced
	// as a whole.
	RouteApp Route = "app"
	// RouteUnknown is the Windows service copy when no record says which
	// file it was copied from, as after an install by an earlier version.
	RouteUnknown Route = "unknown"
)

// ServiceCopyRecord is the file beside the Windows service copy that names
// the owngit.exe it was copied from, so that the running service can tell
// how that program is updated.
const ServiceCopyRecord = "installed-from.txt"

// Install describes the OwnGit program: its route and the file the route put
// on this computer.
type Install struct {
	Route Route
	// Executable is the program file with links resolved. For the Windows
	// service copy it is the file the copy was made from.
	Executable string
	// Prefix is the Homebrew prefix of RouteHomebrew.
	Prefix string
	// Package is the pacman package of RoutePacman.
	Package string
}

// ClassifyExecutable tells the route from where the file is. A path shows
// Homebrew, npm and an app bundle; pacman needs its database, so the caller
// asks it about an archive on Linux (see Install.OwnedBy).
func ClassifyExecutable(path string) Install {
	install := Install{Route: RouteArchive, Executable: path}
	switch {
	case HomebrewPrefix(path) != "":
		install.Route, install.Prefix = RouteHomebrew, HomebrewPrefix(path)
	case NPMExecutable(path):
		install.Route = RouteNPM
	case strings.Contains(filepath.ToSlash(path), ".app/Contents/"):
		install.Route = RouteApp
	}
	return install
}

// OwnedBy returns the install as part of a pacman package when package is
// not empty.
func (install Install) OwnedBy(pkg string) Install {
	if install.Route == RouteArchive && pkg != "" {
		install.Route, install.Package = RoutePacman, pkg
	}
	return install
}

// Platform is the release target an update downloads for and how the
// running service picks up the new program.
type Platform struct {
	GOOS, GOARCH string
	// Service is true when a service registration starts OwnGit for this
	// account, so the update ends with "owngit service install", which
	// rewrites the registration and restarts the service with the new
	// program. Without it the owner restarts OwnGit where it runs.
	Service bool
	// ServiceRunsFile is true on Windows when the service starts
	// Install.Executable itself (a sign-in task), which Windows keeps from
	// being replaced while it runs, so an npm update stops the service
	// first.
	ServiceRunsFile bool
	// Sudo is true when this account cannot write the folder of an archive
	// install, so replacing the file needs root.
	Sudo bool
}

// releaseDownloads is where release assets are.
const releaseDownloads = "https://github.com/juliankang4/owngit/releases/download/v"

// releaseTargets are the platforms with a release archive, and its format.
var releaseTargets = map[string]string{
	"darwin/arm64": "tar.gz", "linux/amd64": "tar.gz", "linux/arm64": "tar.gz", "windows/amd64": "zip",
}

// UpdateCommand is the one command that updates this install to version, or
// "" when the route has no command: an app bundle, an unknown service copy,
// or an archive for a platform without a release archive. OwnGit never runs
// it; the owner does.
func (install Install) UpdateCommand(version string, platform Platform) string {
	windows := platform.GOOS == "windows"
	owngit := "owngit"
	var steps []string
	switch install.Route {
	case RouteHomebrew:
		owngit = shellWord(filepath.Join(install.Prefix, "bin", "owngit"))
		steps = append(steps, shellWord(filepath.Join(install.Prefix, "bin", "brew"))+" upgrade owngit")
	case RouteNPM:
		// npm replaces the file in place, which Windows refuses while the
		// service runs it.
		if windows && platform.Service && platform.ServiceRunsFile {
			steps = append(steps, "owngit service stop")
		}
		steps = append(steps, "npm install -g owngit@"+version)
	case RoutePacman:
		// The PKGBUILD attached to the release builds the same package.
		steps = append(steps, "(cd \"$(mktemp -d)\" && curl -fLO "+releaseDownloads+version+"/PKGBUILD && makepkg -si)")
	case RouteArchive:
		format, ok := releaseTargets[platform.GOOS+"/"+platform.GOARCH]
		if !ok {
			return ""
		}
		name := "owngit_" + version + "_" + platform.GOOS + "_" + platform.GOARCH
		url := releaseDownloads + version + "/" + name + "." + format
		if windows {
			// A running program cannot be replaced on Windows, so the new
			// release gets its own folder beside this one, named like the
			// archive, and its service install moves the service to it.
			folder := filepath.Join(filepath.Dir(filepath.Dir(install.Executable)), name)
			owngit = "& " + powerShellQuote(filepath.Join(folder, "owngit.exe"))
			steps = append(steps,
				"Invoke-WebRequest "+powerShellQuote(url)+" -OutFile "+powerShellQuote(folder+".zip"),
				"Expand-Archive "+powerShellQuote(folder+".zip")+" "+powerShellQuote(folder))
			break
		}
		// The new file replaces this one by rename, which a running
		// program allows, so the path the service starts stays the same.
		owngit = shellWord(install.Executable)
		move := "mv -f \"$d/owngit\" " + owngit
		if platform.Sudo {
			move = "sudo " + move
		}
		steps = append(steps, "d=$(mktemp -d) && curl -fLo \"$d/owngit."+format+"\" "+url+
			" && tar -xzf \"$d/owngit."+format+"\" -C \"$d\" owngit && "+move)
	default:
		return ""
	}
	if platform.Service {
		steps = append(steps, owngit+" service install")
	}
	if windows {
		// Windows PowerShell 5.1 has no &&.
		return strings.Join(steps, "; ")
	}
	return strings.Join(steps, " && ")
}

// RemoveCommand is the command that removes the program files, which OwnGit
// leaves to whoever put them here, or "" when there is none to give.
func (install Install) RemoveCommand(goos string, sudo bool) string {
	switch install.Route {
	case RouteHomebrew:
		return shellWord(filepath.Join(install.Prefix, "bin", "brew")) + " uninstall owngit"
	case RouteNPM:
		return "npm uninstall -g owngit"
	case RoutePacman:
		return "sudo pacman -R " + install.Package
	case RouteArchive:
		if goos == "windows" {
			return "Remove-Item " + powerShellQuote(install.Executable)
		}
		if sudo {
			return "sudo rm " + shellWord(install.Executable)
		}
		return "rm " + shellWord(install.Executable)
	}
	return ""
}

// shellWord quotes a word for a POSIX shell only when it needs quoting, so
// ordinary paths stay easy to read.
func shellWord(word string) string {
	if word != "" && strings.Trim(word, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return word
	}
	return shellQuote(word)
}

// powerShellQuote quotes a word for PowerShell.
func powerShellQuote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", "''") + "'"
}
