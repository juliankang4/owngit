//go:build !windows

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func assertWorkflowSecretsPrivate(t *testing.T, path string) {
	t.Helper()
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Dir(path), 0o700}, {path, 0o600}} {
		info, err := os.Stat(item.path)
		noErr(t, err)
		if info.Mode().Perm() != item.mode {
			t.Fatalf("private secret path mode=%o want=%o", info.Mode().Perm(), item.mode)
		}
	}
}
