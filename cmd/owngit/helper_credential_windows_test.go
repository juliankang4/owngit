//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/state"
)

func TestWindowsReservedTokenFileDeniesReplacementAndStaysPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	reserved, err := reservePrivateTokenFile(path)
	noErr(t, err)
	if err := os.Rename(path, path+".moved"); err == nil {
		_ = reserved.preserve()
		t.Fatal("reserved token file allowed path replacement")
	}
	if err := reserved.write("token-value"); err != nil {
		_ = reserved.preserve()
		t.Fatal(err)
	}
	noErr(t, reserved.preserve())
	noErr(t, state.ValidatePrivateFile(path))
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "token-value\n" {
		t.Fatalf("token file=%q err=%v", content, err)
	}
}
