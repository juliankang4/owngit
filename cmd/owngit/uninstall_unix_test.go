//go:build !windows

package main

import (
	"testing"

	"owngit/internal/service"
)

// A package manager's files are left to it with its command; a hand-placed
// program stays, with the command that deletes it. Here this account cannot
// write the program folders, so npm and rm need sudo.
func TestProgramStaysLine(t *testing.T) {
	for _, tc := range []struct {
		install service.Install
		want    string
	}{
		{service.ClassifyExecutable("/opt/homebrew/Cellar/owngit/1.1.2/bin/owngit"), "The program belongs to Homebrew, which removes it: /opt/homebrew/bin/brew uninstall owngit"},
		{service.ClassifyExecutable("/usr/lib/node_modules/owngit/node_modules/owngit-linux-x64/bin/owngit"), "The program belongs to npm, which removes it: sudo npm uninstall -g owngit"},
		{service.ClassifyExecutable("/usr/bin/owngit").OwnedBy("owngit-bin"), "The program belongs to the pacman package owngit-bin, which pacman removes: sudo pacman -R owngit-bin"},
		{service.ClassifyExecutable("/usr/local/bin/owngit"), "The program /usr/local/bin/owngit stays, because OwnGit did not put it there. To remove it, delete it (and the folder you unpacked it into, if you made one): sudo rm /usr/local/bin/owngit"},
		{service.ClassifyExecutable("/usr/local/bin/owngit").RecordedAs("container"), `The program is part of the OwnGit container image. To remove it, remove the container on the computer that runs it, with "docker compose down" in the folder of its compose.yaml; the data volume stays.`},
	} {
		if got := programStaysLine(tc.install, "linux", true); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.install.Route, got, tc.want)
		}
	}
}
