//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/service"
)

// Install detection asks only the pacman at its fixed path, and only when
// root alone can change it: a pacman found earlier on PATH, or one that this
// account can change, is never run.
func TestPacmanIsAskedOnlyAtItsFixedRootOwnedPath(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	fake := filepath.Join(dir, "pacman")
	noErr(t, os.WriteFile(fake, []byte("#!/bin/sh\ntouch '"+marker+"'\necho owngit-bin\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	program := filepath.Join(dir, "owngit")
	noErr(t, os.WriteFile(program, nil, 0o755))

	for name, answer := range map[string]string{
		"fixed path": askPacman(pacmanPath, program),
		"own file":   askPacman(fake, program),
	} {
		if answer != "" {
			t.Errorf("%s: pacman answered %q for a file no package holds", name, answer)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("a pacman that is not root's ran (%v)", err)
	}
}

// A command needs sudo exactly when this account cannot write the folder it
// changes, for an archive program and for npm alike.
func TestNeedsSudoWhenTheProgramFolderIsNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write every folder")
	}
	for _, name := range []string{"archive", "npm"} {
		root := t.TempDir()
		program := filepath.Join(root, "bin", "owngit")
		if name == "npm" {
			program = filepath.Join(root, "lib", "node_modules", "owngit", "node_modules", "owngit-linux-x64", "bin", "owngit")
		}
		noErr(t, os.MkdirAll(filepath.Dir(program), 0o755))
		install := service.ClassifyExecutable(program)
		if string(install.Route) != name || needsSudo(install) {
			t.Fatalf("%s: route %q, sudo in a writable folder", name, install.Route)
		}
		folder := install.ProgramFolder()
		noErr(t, os.Chmod(folder, 0o555))
		t.Cleanup(func() { _ = os.Chmod(folder, 0o755) })
		if !needsSudo(install) {
			t.Errorf("%s: no sudo for %s, which this account cannot write", name, folder)
		}
	}
}
