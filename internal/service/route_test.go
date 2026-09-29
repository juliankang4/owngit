//go:build !windows

package service

import (
	"os/exec"
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
}

// Each route has one update command: the package manager's own, or for an
// archive the download that replaces this file. Only a service that starts
// this program gets "owngit service install" to restart it.
func TestUpdateCommand(t *testing.T) {
	brew := ClassifyExecutable("/opt/homebrew/Cellar/owngit/1.1.2/bin/owngit")
	npm := ClassifyExecutable("/usr/lib/node_modules/owngit/node_modules/owngit-linux-x64/bin/owngit")
	pacman := ClassifyExecutable("/usr/bin/owngit").OwnedBy("owngit-bin")
	archive := ClassifyExecutable("/home/you/bin/owngit")
	spaced := ClassifyExecutable("/Users/you/My Tools/owngit")
	mac := Platform{GOOS: "darwin", GOARCH: "arm64"}
	linux := Platform{GOOS: "linux", GOARCH: "amd64"}
	withService := func(platform Platform) Platform { platform.Service = true; return platform }
	root := linux
	root.Sudo = true
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
		{"pacman service", pacman, withService(linux), `(cd "$(mktemp -d)" && curl -fLO https://github.com/juliankang4/owngit/releases/download/v1.1.3/PKGBUILD && makepkg -si) && owngit service install`},
		{"archive", archive, linux, `d=$(mktemp -d) && curl -fLo "$d/owngit.tar.gz" https://github.com/juliankang4/owngit/releases/download/v1.1.3/owngit_1.1.3_linux_amd64.tar.gz && tar -xzf "$d/owngit.tar.gz" -C "$d" owngit && mv -f "$d/owngit" /home/you/bin/owngit`},
		{"archive as root", archive, withService(root), `d=$(mktemp -d) && curl -fLo "$d/owngit.tar.gz" https://github.com/juliankang4/owngit/releases/download/v1.1.3/owngit_1.1.3_linux_amd64.tar.gz && tar -xzf "$d/owngit.tar.gz" -C "$d" owngit && sudo mv -f "$d/owngit" /home/you/bin/owngit && /home/you/bin/owngit service install`},
		{"archive with a space", spaced, withService(mac), `d=$(mktemp -d) && curl -fLo "$d/owngit.tar.gz" https://github.com/juliankang4/owngit/releases/download/v1.1.3/owngit_1.1.3_darwin_arm64.tar.gz && tar -xzf "$d/owngit.tar.gz" -C "$d" owngit && mv -f "$d/owngit" '/Users/you/My Tools/owngit' && '/Users/you/My Tools/owngit' service install`},
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
		{ClassifyExecutable("/usr/bin/owngit").OwnedBy("owngit-bin"), false, "sudo pacman -R owngit-bin"},
		{ClassifyExecutable("/usr/local/bin/owngit"), true, "sudo rm /usr/local/bin/owngit"},
		{ClassifyExecutable("/home/you/it's/owngit"), false, `rm '/home/you/it'\''s/owngit'`},
		{ClassifyExecutable("/Applications/OwnGit.app/Contents/Helpers/owngit"), false, ""},
	} {
		if got := tc.install.RemoveCommand("linux", tc.sudo); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.install, got, tc.want)
		}
	}
}
