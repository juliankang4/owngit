//go:build !windows

package state

import (
	"fmt"
	"os"

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

func openSourceHandle(path string, _ bool) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open protected state entry", Path: path, Err: err}
	}
	return os.NewFile(uintptr(descriptor), path), nil
}
