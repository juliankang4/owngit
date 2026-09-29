package service

import (
	"path/filepath"
	"regexp"
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

// OwnedBy returns the install as part of the pacman package pkg when pkg is
// a valid package name. Anything else, including "", several lines or shell
// syntax, is not a package, so the install keeps its route.
func (install Install) OwnedBy(pkg string) Install {
	if install.Route == RouteArchive && packageName.MatchString(pkg) {
		install.Route, install.Package = RoutePacman, pkg
	}
	return install
}

// packageName is a pacman package name as makepkg accepts it: letters,
// digits and @._+-, not starting with a hyphen or a dot.
var packageName = regexp.MustCompile(`^[A-Za-z0-9@_+][A-Za-z0-9@._+-]{0,254}$`)

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
	// Root is true when root runs the command. makepkg refuses root, so a
	// pacman install gets no command then; the owner's normal account asks
	// for it.
	Root bool
}

// releaseDownloads is where release assets are.
const releaseDownloads = "https://github.com/juliankang4/owngit/releases/download/v"

// releaseTargets are the platforms with a release archive, and its format.
var releaseTargets = map[string]string{
	"darwin/arm64": "tar.gz", "linux/amd64": "tar.gz", "linux/arm64": "tar.gz", "windows/amd64": "zip",
}

// ReleasePackage is the pacman package that the PKGBUILD of each release
// builds.
const ReleasePackage = "owngit-bin"

// windowsReleaseFolder is where a Windows archive update unpacks version: a
// running program cannot be replaced on Windows, so the new release gets its
// own folder beside this one, named like the archive.
func windowsReleaseFolder(install Install, version string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(install.Executable)), "owngit_"+version+"_windows_amd64")
}

// StartAfterUpdate is the program the owner starts after the update command
// when no service restarts OwnGit, or "" when restarting OwnGit where it
// runs starts the new version. Only a Windows archive update puts the new
// program somewhere else.
func (install Install) StartAfterUpdate(version string, platform Platform) string {
	if platform.Service || install.Route != RouteArchive || platform.GOOS != "windows" || install.UpdateCommand(version, platform) == "" {
		return ""
	}
	return filepath.Join(windowsReleaseFolder(install, version), "owngit.exe")
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
		// The PKGBUILD attached to the release builds ReleasePackage and
		// replaces any other package that provides owngit, so another
		// package gets no command. makepkg refuses root.
		if install.Package != ReleasePackage || platform.Root {
			return ""
		}
		steps = append(steps, "(cd \"$(mktemp -d)\" && curl -fLO "+releaseDownloads+version+"/PKGBUILD && makepkg -si)")
	case RouteArchive:
		format, ok := releaseTargets[platform.GOOS+"/"+platform.GOARCH]
		if !ok {
			return ""
		}
		name := "owngit_" + version + "_" + platform.GOOS + "_" + platform.GOARCH
		url := releaseDownloads + version + "/" + name + "." + format
		if windows {
			folder := windowsReleaseFolder(install, version)
			owngit = "& " + powerShellQuote(filepath.Join(folder, "owngit.exe"))
			steps = append(steps,
				"Invoke-WebRequest -UseBasicParsing "+powerShellQuote(url)+" -OutFile "+powerShellQuote(folder+".zip"),
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
		return powerShellChain(steps)
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
		return "sudo pacman -R " + shellWord(install.Package)
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

// powerShellChain runs each step only when the one before it succeeded, the
// way && does in a POSIX shell, in both Windows PowerShell 5.1, which has no
// &&, and PowerShell 7. $? is false after a failed cmdlet and after a program
// that exits with a nonzero status. The steps nest, because $? after a
// skipped "if" block is true again.
func powerShellChain(steps []string) string {
	chain := steps[len(steps)-1]
	for index := len(steps) - 2; index >= 0; index-- {
		chain = steps[index] + "; if ($?) { " + chain + " }"
	}
	return chain
}

// shellWord quotes a word for a POSIX shell only when it needs quoting, so
// ordinary paths stay easy to read.
func shellWord(word string) string {
	if word != "" && strings.Trim(word, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return word
	}
	return shellQuote(word)
}

// powerShellQuote quotes a word for PowerShell. PowerShell ends a
// single-quoted string at any of the single-quote characters U+0027 and
// U+2018 to U+201B, so each is doubled, as PowerShell's own escaping does.
func powerShellQuote(word string) string {
	var quoted strings.Builder
	quoted.WriteByte('\'')
	for _, r := range word {
		quoted.WriteRune(r)
		switch r {
		case '\'', '\u2018', '\u2019', '\u201a', '\u201b':
			quoted.WriteRune(r)
		}
	}
	quoted.WriteByte('\'')
	return quoted.String()
}
