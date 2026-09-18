//go:build !windows

package main

import (
	"os"
	"testing"
)

func makePasswordFileBroad(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}
