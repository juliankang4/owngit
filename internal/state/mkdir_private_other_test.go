//go:build !darwin && !windows

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMkdirPrivateUsesOwnerOnlyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "staging")
	noErr(t, MkdirPrivate(path))
	info, err := os.Stat(path)
	noErr(t, err)
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("private directory mode=%o", info.Mode().Perm())
	}
}
