//go:build !windows

package state

import (
	"errors"
	"os"
)

// CreatePrivateFile creates a new owner-only file and keeps its handle open.
func CreatePrivateFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

func ProtectPrivatePath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}

// ProtectPrivateHandle applies the private mode to the open file, so a
// pathname replacement cannot change what was protected.
func ProtectPrivateHandle(file *os.File, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return file.Chmod(mode)
}

func ValidatePrivateFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return validatePrivateFileInfo(info)
}

// ValidatePrivateFileHandle validates the open file rather than reopening its
// path, which may now name a replacement.
func ValidatePrivateFileHandle(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return validatePrivateFileInfo(info)
}

func validatePrivateFileInfo(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return errors.New("private input must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("private input must not be readable by group or others")
	}
	return nil
}
