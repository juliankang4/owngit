//go:build !windows

package state

import (
	"errors"
	"fmt"
	"os"
	"strings"
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
	return validatePrivateFileInfo(path, info)
}

// ValidatePrivateInputFile checks a secret file that the owner may have made
// by hand, such as a password or token file. On Unix it is the same check as
// ValidatePrivateFile.
func ValidatePrivateInputFile(path string) error {
	return ValidatePrivateFile(path)
}

// ValidatePrivateFileHandle validates the open file rather than reopening its
// path, which may now name a replacement.
func ValidatePrivateFileHandle(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return validatePrivateFileInfo(file.Name(), info)
}

// validatePrivateFileInfo refuses a file that its group or other users can
// access, with a *NotPrivateError that names the mode and the chmod command.
func validatePrivateFileInfo(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return errors.New("private input must be a regular file")
	}
	permissions := info.Mode().Perm()
	if permissions&0o077 == 0 {
		return nil
	}
	var who []string
	if permissions&0o070 != 0 {
		who = append(who, "its group")
	}
	if permissions&0o007 != 0 {
		who = append(who, "all other users")
	}
	return &NotPrivateError{
		Problem: fmt.Sprintf("its mode %04o gives access to %s", permissions, strings.Join(who, " and ")),
		Fix:     "chmod 600 " + shellQuote(operandPath(path)),
	}
}

// operandPath keeps a relative path that starts with "-" from being read as
// options. "chmod 600 -- PATH" would not do: BSD chmod on macOS stops reading
// options at the mode, so it takes "--" as a file name.
func operandPath(path string) string {
	if strings.HasPrefix(path, "-") {
		return "./" + path
	}
	return path
}

// shellQuote quotes path for a POSIX shell.
func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}
