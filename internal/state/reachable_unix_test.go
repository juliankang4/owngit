//go:build darwin || linux

package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExposureCheckRejectsReplacedAncestorPath(t *testing.T) {
	base := t.TempDir()
	parentPath := filepath.Join(base, "parent")
	targetPath := filepath.Join(parentPath, "repositories")
	noErr(t, os.Mkdir(parentPath, 0o755))
	noErr(t, os.Mkdir(targetPath, 0o777))
	noErr(t, os.Chmod(targetPath, 0o777))
	target, err := os.Open(targetPath)
	noErr(t, err)
	defer target.Close()
	info, err := target.Stat()
	noErr(t, err)

	noErr(t, os.Rename(parentPath, filepath.Join(base, "moved")))
	noErr(t, os.Mkdir(parentPath, 0o700))
	noErr(t, os.Mkdir(targetPath, 0o777))
	noErr(t, os.Chmod(targetPath, 0o777))
	if exposed, err := ExposedToOtherAccounts(targetPath, target, info); err == nil || exposed {
		t.Fatalf("replaced path exposed=%v err=%v", exposed, err)
	}
}
