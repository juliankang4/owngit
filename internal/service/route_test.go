//go:build !windows

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The route follows from where the file is; pacman ownership is added by the
// caller that asked pacman.
func TestClassifyExecutable(t *testing.T) {
	for _, tc := range []struct {
		path  string
		route Route
	}{
		{"/opt/homebrew/Cellar/owngit/1.1.2/bin/owngit", RouteHomebrew},
		{"/home/linuxbrew/.linuxbrew/Cellar/owngit/1.1.2/bin/owngit", RouteHomebrew},
		{"/usr/local/lib/node_modules/owngit/node_modules/owngit-linux-x64/bin/owngit", RouteNPM},
		{"/Applications/OwnGit.app/Contents/Helpers/owngit", RouteApp},
		{"/usr/local/bin/owngit", RouteArchive},
		{"/home/you/owngit_1.1.2_linux_amd64/owngit", RouteArchive},
	} {
		if got := ClassifyExecutable(tc.path); got.Route != tc.route || got.Executable != tc.path {
			t.Errorf("%s: %+v, want %s", tc.path, got, tc.route)
		}
	}
	if got := ClassifyExecutable("/usr/bin/owngit").OwnedBy("owngit-bin"); got.Route != RoutePacman || got.Package != "owngit-bin" {
		t.Errorf("pacman: %+v", got)
	}
	if got := ClassifyExecutable("/opt/homebrew/Cellar/owngit/1.1.2/bin/owngit").OwnedBy("other"); got.Route != RouteHomebrew {
		t.Errorf("a package manager's own route changed: %+v", got)
	}
	// Only a valid package name makes the file part of a package.
	for _, pkg := range []string{"owngit-bin", "owngit-git", "lib32-x+y@1.0_z"} {
		if got := ClassifyExecutable("/usr/bin/owngit").OwnedBy(pkg); got.Route != RoutePacman || got.Package != pkg {
			t.Errorf("%q: %+v", pkg, got)
		}
	}
	for _, pkg := range []string{"", "owngit-bin\nother", "a b", "x;touch y", "$(id)", "-x", ".x", "x'y"} {
		if got := ClassifyExecutable("/usr/bin/owngit").OwnedBy(pkg); got.Route != RouteArchive || got.Package != "" {
			t.Errorf("%q was taken as a package: %+v", pkg, got)
		}
	}
}

// Each route has one update command: the package manager's own, or for an
// archive the release's installer, which checks the download against
// SHA256SUMS and replaces this file, using sudo by itself when this account
// cannot write the folder. Only a service that starts this program gets
// "owngit service install" to restart it; the installer skips it with
// --no-service.
func TestUpdateCommand(t *testing.T) {
	brew := ClassifyExecutable("/opt/homebrew/Cellar/owngit/1.1.2/bin/owngit")
	npm := ClassifyExecutable("/usr/lib/node_modules/owngit/node_modules/owngit-linux-x64/bin/owngit")
	pacman := ClassifyExecutable("/usr/bin/owngit").OwnedBy("owngit-bin")
	archive := ClassifyExecutable("/home/you/bin/owngit")
	spaced := ClassifyExecutable("/Users/you/My Tools/owngit")
	mac := Platform{GOOS: "darwin", GOARCH: "arm64"}
	linux := Platform{GOOS: "linux", GOARCH: "amd64"}
	withService := func(platform Platform) Platform { platform.Service = true; return platform }
	sudo := linux
	sudo.Sudo = true
	for _, tc := range []struct {
		name     string
		install  Install
		platform Platform
		want     string
	}{
		{"homebrew", brew, mac, "/opt/homebrew/bin/brew upgrade owngit"},
		{"homebrew service", brew, withService(mac), "/opt/homebrew/bin/brew upgrade owngit && /opt/homebrew/bin/owngit service install"},
		{"npm", npm, linux, "npm install -g owngit@1.1.3"},
		{"npm service", npm, withService(linux), "npm install -g owngit@1.1.3 && owngit service install"},
		{"npm with sudo", npm, withService(sudo), "sudo npm install -g owngit@1.1.3 && owngit service install"},
		{"pacman service", pacman, withService(linux), `(cd "$(mktemp -d)" && curl --proto '=https' --proto-redir '=https' -fLO https://github.com/juliankang4/owngit/releases/download/v1.1.3/PKGBUILD && makepkg -si) && owngit service install`},
		{"archive", archive, linux, "curl --proto '=https' --proto-redir '=https' -fsSL https://raw.githubusercontent.com/juliankang4/owngit/v1.1.3/packaging/installer/install.sh | sh -s -- --version 1.1.3 --to /home/you/bin/owngit --no-service"},
		{"archive with sudo", archive, withService(sudo), "curl --proto '=https' --proto-redir '=https' -fsSL https://raw.githubusercontent.com/juliankang4/owngit/v1.1.3/packaging/installer/install.sh | sh -s -- --version 1.1.3 --to /home/you/bin/owngit"},
		{"archive with a space", spaced, withService(mac), "curl --proto '=https' --proto-redir '=https' -fsSL https://raw.githubusercontent.com/juliankang4/owngit/v1.1.3/packaging/installer/install.sh | sh -s -- --version 1.1.3 --to '/Users/you/My Tools/owngit'"},
		{"pacman as root", pacman, Platform{GOOS: "linux", GOARCH: "amd64", Service: true, Root: true}, ""},
		{"another pacman package", ClassifyExecutable("/usr/bin/owngit").OwnedBy("owngit-git"), withService(linux), ""},
		{"archive without a release target", archive, Platform{GOOS: "darwin", GOARCH: "amd64"}, ""},
		{"app", ClassifyExecutable("/Applications/OwnGit.app/Contents/Helpers/owngit"), withService(mac), ""},
		{"unknown service copy", Install{Route: RouteUnknown}, withService(linux), ""},
	} {
		got := tc.install.UpdateCommand("1.1.3", tc.platform)
		if got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
			continue
		}
		if got == "" {
			continue
		}
		// The owner pastes it into a shell, so it must parse as one.
		if output, err := exec.Command("sh", "-n", "-c", got).CombinedOutput(); err != nil {
			t.Errorf("%s: sh -n: %v: %s", tc.name, err, output)
		}
	}
}

// Pasted into a shell, the archive command hands the installer this
// program's path as one argument, spaces and quotes included. A stub stands
// in for the downloaded installer, so nothing is downloaded or installed.
func TestArchiveCommandPassesThePathToTheInstaller(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "install.sh")
	got := filepath.Join(dir, "arguments")
	noErr(t, os.WriteFile(stub, []byte("printf '%s\\n' \"$@\" >"+shellQuote(got)+"\n"), 0o644))
	program := "/Users/you/O'Brien Tools/owngit"
	command := ClassifyExecutable(program).UpdateCommand("1.1.3", Platform{GOOS: "darwin", GOARCH: "arm64"})
	download := httpsOnly + "-fsSL " + installerSource + "1.1.3/packaging/installer/install.sh"
	if !strings.HasPrefix(command, download+" | ") {
		t.Fatalf("command %s", command)
	}
	command = "cat " + shellQuote(stub) + strings.TrimPrefix(command, download)
	if output, err := exec.Command("sh", "-c", command).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", command, err, output)
	}
	arguments, err := os.ReadFile(got)
	noErr(t, err)
	if want := "--version\n1.1.3\n--to\n" + program + "\n--no-service\n"; string(arguments) != want {
		t.Errorf("the installer got %q, want %q", arguments, want)
	}
}

// OwnGit leaves the program files to whoever put them there and names the
// command that removes them.
func TestRemoveCommand(t *testing.T) {
	for _, tc := range []struct {
		install Install
		sudo    bool
		want    string
	}{
		{ClassifyExecutable("/opt/homebrew/Cellar/owngit/1.1.2/bin/owngit"), false, "/opt/homebrew/bin/brew uninstall owngit"},
		{ClassifyExecutable("/usr/lib/node_modules/owngit/node_modules/owngit-linux-x64/bin/owngit"), false, "npm uninstall -g owngit"},
		{ClassifyExecutable("/usr/lib/node_modules/owngit/node_modules/owngit-linux-x64/bin/owngit"), true, "sudo npm uninstall -g owngit"},
		{ClassifyExecutable("/usr/bin/owngit").OwnedBy("owngit-bin"), false, "sudo pacman -R owngit-bin"},
		{ClassifyExecutable("/usr/local/bin/owngit"), true, "sudo rm /usr/local/bin/owngit"},
		{ClassifyExecutable("/home/you/it's/owngit"), false, `rm '/home/you/it'\''s/owngit'`},
		{ClassifyExecutable("/Applications/OwnGit.app/Contents/Helpers/owngit"), false, ""},
		// Every value in a printed command is quoted for its shell.
		{Install{Route: RoutePacman, Package: "a b"}, false, "sudo pacman -R 'a b'"},
	} {
		if got := tc.install.RemoveCommand("linux", tc.sudo); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.install, got, tc.want)
		}
	}
}

// The program folder is what the update and remove commands change, so it
// decides whether they need sudo.
func TestProgramFolder(t *testing.T) {
	for path, want := range map[string]string{
		"/usr/local/bin/owngit": "/usr/local/bin",
		"/usr/local/lib/node_modules/owngit/node_modules/owngit-linux-x64/bin/owngit": "/usr/local/lib/node_modules",
		"/home/you/.npm-global/lib/node_modules/owngit-linux-x64/bin/owngit":          "/home/you/.npm-global/lib/node_modules",
		"/opt/homebrew/Cellar/owngit/1.1.2/bin/owngit":                                "",
		"/Applications/OwnGit.app/Contents/Helpers/owngit":                            "",
	} {
		if got := ClassifyExecutable(path).ProgramFolder(); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
	if got := ClassifyExecutable("/usr/bin/owngit").OwnedBy("owngit-bin").ProgramFolder(); got != "" {
		t.Errorf("pacman: %q, want none", got)
	}
}
