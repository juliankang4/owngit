//go:build windows

package importsync

import (
	"os"

	"golang.org/x/sys/windows"
)

func refPathIsIndirect(path string, _ os.FileInfo) (bool, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}
