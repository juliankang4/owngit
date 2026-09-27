//go:build !windows

package state

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// protectionFingerprint describes the permission state that a refusal must
// leave unchanged. It is compared as an opaque string.
func protectionFingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("mode=%04o", info.Mode().Perm()), nil
}

func resolveStatePath(path string) (string, error) { return filepath.EvalSymlinks(path) }

func requireAcceptableStateOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("state directory must be owned by this account or root; run the command as its owner")
	}
	return nil
}

func openSourceHandle(path string, _ bool) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open protected state entry", Path: path, Err: err}
	}
	return os.NewFile(uintptr(descriptor), path), nil
}
