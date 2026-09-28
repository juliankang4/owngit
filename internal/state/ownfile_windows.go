//go:build windows

package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// OpenOwnFile opens the file name in the held directory dir, with flag
// os.O_RDONLY, os.O_WRONLY or os.O_RDWR, and os.O_APPEND, without following
// a reparse point at that name. With os.O_CREATE it creates the file when
// the name is free, and never opens what another process put there in
// between. A file that exists must be a regular file of this account (or
// its token owner) with no other name, so a caller that then truncates,
// locks or protects it cannot change another file. OpenOwnFile never
// truncates; a caller truncates the returned file once it is known.
func OpenOwnFile(dir *os.File, name string, flag int) (*os.File, error) {
	path := filepath.Join(dir.Name(), name)
	var access uint32
	switch {
	case flag&os.O_RDWR != 0:
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	case flag&os.O_WRONLY != 0 && flag&os.O_APPEND != 0:
		access = windows.FILE_APPEND_DATA
	case flag&os.O_WRONLY != 0:
		access = windows.GENERIC_WRITE
	default:
		access = windows.GENERIC_READ
	}
	access |= windows.FILE_READ_ATTRIBUTES | windows.FILE_WRITE_ATTRIBUTES | windows.READ_CONTROL
	open := func(disposition uint32) (*os.File, error) {
		return openAt(dir, path, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, disposition, windows.FILE_NON_DIRECTORY_FILE, "open")
	}
	file, err := open(windows.FILE_OPEN)
	if errors.Is(err, fs.ErrNotExist) && flag&os.O_CREATE != 0 {
		file, err = open(windows.FILE_CREATE)
	}
	if err != nil {
		return nil, err
	}
	if err := requireOwnFile(file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// requireOwnFile refuses an open file that OpenOwnFile must not use.
func requireOwnFile(file *os.File) error {
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &information); err != nil {
		return &os.PathError{Op: "stat", Path: file.Name(), Err: err}
	}
	owned, err := OwnedByCurrentUser(file)
	switch {
	case information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0:
		return fmt.Errorf("%s is not a regular file", file.Name())
	case err != nil:
		return err
	case !owned:
		return fmt.Errorf("%s belongs to another account", file.Name())
	case information.NumberOfLinks != 1:
		return fmt.Errorf("%s has another name as well, so it may be another file", file.Name())
	}
	return nil
}
