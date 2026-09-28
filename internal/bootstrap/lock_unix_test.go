//go:build !windows

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The setup lock is made private only as a file of this account with one
// name, like every other lock: a second name of another file put there
// leaves that file's mode as it was.
func TestSetupLockChangesNoFileAtItsName(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(t.TempDir(), "program")
	noErr(t, os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755))
	noErr(t, os.Chmod(target, 0o755))
	noErr(t, os.Link(target, filepath.Join(directory, lockFileName)))
	if unlock, err := AcquireSetupLock(context.Background(), directory); err == nil {
		unlock()
		t.Error("the setup lock was taken through a second name")
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("the file with the second name changed: %v %v", info.Mode(), err)
	}
}
