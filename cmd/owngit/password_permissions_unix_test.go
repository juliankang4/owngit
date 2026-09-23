//go:build !windows

package main

import (
	"os"
	"testing"
)

func makePasswordFileBroad(t *testing.T, path string) {
	t.Helper()
	noErr(t, os.Chmod(path, 0o644))
}
