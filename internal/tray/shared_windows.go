//go:build windows

package tray

import (
	"os"

	"golang.org/x/sys/windows"
)

// readSharedFile reads at most limit bytes of the file at path. The file is
// opened with delete sharing and closed at once, so the server can replace
// it by rename while the icon reads it.
func readSharedFile(path string, limit int64) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return readLimited(file, path, limit)
}
