//go:build !windows

package state

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// CreatePrivateFile creates a new owner-only file and keeps its handle open.
func CreatePrivateFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := ProtectPrivateHandle(file, false); err != nil {
		return nil, errors.Join(err, file.Close(), os.Remove(path))
	}
	return file, nil
}

// ProtectPrivatePath opens a final non-link entry, then protects that handle.
func protectSQLiteFilesAfterOpen(string) error { return nil }

func ProtectPrivatePath(path string, directory bool) error {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return &os.PathError{Op: "open private path without following a link", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(descriptor), path)
	defer file.Close()
	return ProtectPrivateHandle(file, directory)
}

// ProtectPrivateHandle applies the private mode to the open file, so a
// pathname replacement cannot change what was protected, and on macOS
// removes its access list like ProtectPrivatePath.
func ProtectPrivateHandle(file *os.File, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	return clearAccessList(file)
}

// OwnedByCurrentUser reports whether the open file or directory belongs to the
// effective user. It reads the held handle, so a pathname replacement cannot
// change the answer.
func OwnedByCurrentUser(file *os.File) (bool, error) {
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, errors.New("file owner is unavailable")
	}
	return int(stat.Uid) == os.Geteuid(), nil
}

func ValidatePrivateFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return validatePrivateFileInfo(path, info)
}

// OpenPrivateInputFile validates a supplied secret and returns the same open
// file for reading. Type, mode, owner identity and (on macOS) the access list
// all come from this held object, so a path replacement cannot change them.
// A link at the final name is followed and its target checked.
func OpenPrivateInputFile(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open private input", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(descriptor), path)
	info, err := file.Stat()
	if err == nil {
		err = validatePrivateFileInfo(path, info)
	}
	if err == nil {
		err = validatePrivateInputOwner(path, info)
	}
	if err == nil {
		err = validatePrivateInputAccessList(file, info)
	}
	if err == nil {
		if err = unix.SetNonblock(descriptor, false); err != nil {
			err = &os.PathError{Op: "open private input", Path: path, Err: err}
		}
	}
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

// ValidatePrivateInputFile checks a secret file that the owner may have made
// by hand, such as a password or token file. Besides the Unix mode check,
// macOS refuses an access list that gives another account read access.
func ValidatePrivateInputFile(path string) error {
	file, err := OpenPrivateInputFile(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func validatePrivateInputOwner(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("file owner is unavailable")
	}
	if int(stat.Uid) == os.Geteuid() || stat.Uid == 0 {
		return nil
	}
	return &PrivateInputOwnerError{Path: path}
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
