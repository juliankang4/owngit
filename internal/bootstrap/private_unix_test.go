//go:build !windows

package bootstrap

import (
	"os"
	"testing"

	"owngit/internal/state"
)

func assertOwnerFilePrivate(t *testing.T, path string) {
	t.Helper()
	noErr(t, state.ValidatePrivateFile(path), "owner setup file is not private")
	info, err := os.Stat(path)
	noErr(t, err)
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("owner setup file mode = %o, want 600", got)
	}
}
