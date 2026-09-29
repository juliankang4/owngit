package recovery

import (
	"errors"

	"golang.org/x/sys/windows"
)

// diskFreeSpace returns the bytes that this account may still write on
// the file system that holds dir, and true as it can tell.
func diskFreeSpace(dir string) (uint64, bool, error) {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, nil, nil); err != nil {
		return 0, false, err
	}
	return available, true, nil
}

func diskFullError(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}
