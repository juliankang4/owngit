//go:build windows

package state

import (
	"path/filepath"
	"testing"
)

func assertWorkflowSecretsPrivate(t *testing.T, path string) {
	t.Helper()
	user, _, err := processIdentity()
	noErr(t, err)
	noErr(t, validateOwnerOnly(filepath.Dir(path), user, true))
	noErr(t, validateOwnerOnly(path, user, false))
}
