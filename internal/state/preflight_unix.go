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

// lookupSourceEntry and openSourceEntry use the entry's path. Removing a file
// on Unix removes its name at once, so an entry is either present or absent;
// the held directory is not needed to tell.
func lookupSourceEntry(_ *os.File, path string) (os.FileInfo, error) {
	return LstatIdentity(path)
}

func openSourceEntry(_ *os.File, path string, metadataOnly bool) (*os.File, error) {
	return openSourceHandle(path, metadataOnly)
}

func openSourceHandle(path string, _ bool) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open protected state entry", Path: path, Err: err}
	}
	return os.NewFile(uintptr(descriptor), path), nil
}
