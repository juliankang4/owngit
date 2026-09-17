//go:build !windows

package state

import (
	"errors"
	"os"
)

func ProtectPrivatePath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}

func ValidatePrivateFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("private input must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("private input must not be readable by group or others")
	}
	return nil
}
