//go:build windows

package bootstrap

import (
	"testing"

	"owngit/internal/state"
)

func assertOwnerFilePrivate(t *testing.T, path string) {
	t.Helper()
	noErr(t, state.ValidatePrivateFile(path), "owner setup file does not have a protected current-user-only ACL")
}
