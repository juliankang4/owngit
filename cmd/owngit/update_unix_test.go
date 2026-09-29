//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
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
