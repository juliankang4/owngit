//go:build !windows

package bootstrap

import (
	"os"
	"testing"

	"owngit/internal/state"
)

func assertOwnerFilePrivate(t *testing.T, path string) {
	t.Helper()
	if err := state.ValidatePrivateFile(path); err != nil {
		t.Fatalf("owner setup file is not private: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("owner setup file mode = %o, want 600", got)
	}
}
