//go:build !windows

package main

import (
	"os"
	"testing"
)

// linkFolder makes link a symbolic link to target.
func linkFolder(t *testing.T, target, link string) {
	t.Helper()
	noErr(t, os.Symlink(target, link))
}
