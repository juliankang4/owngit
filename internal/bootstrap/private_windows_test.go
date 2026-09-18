//go:build windows

package bootstrap

import (
	"testing"

	"owngit/internal/state"
)

func assertOwnerFilePrivate(t *testing.T, path string) {
	t.Helper()
	if err := state.ValidatePrivateFile(path); err != nil {
		t.Fatalf("owner setup file does not have a protected current-user-only ACL: %v", err)
	}
}
