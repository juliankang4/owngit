//go:build windows

package state

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// MkdirPrivate creates a directory with its final protected owner-only DACL,
// so no inherited grant is present while the directory name is visible.
func MkdirPrivate(path string) error {
	descriptor, err := privateFolderDescriptor()
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	if err := windows.CreateDirectory(name, attributes); err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}
