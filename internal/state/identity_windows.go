//go:build windows

package state

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// LstatIdentity inspects path like os.Lstat, without following a final link or
// reparse point, and records the file identity at the time of the call.
//
// os.SameFile compares identities, and a later comparison is only meaningful
// if the identity belongs to the object that was inspected. A path-based
// os.Lstat of an ordinary Windows file or directory records only the path;
// os.SameFile looks up the volume serial and file index from that path when it
// first compares, after a replacement may have reused the name. A handle-based
// Stat records them immediately. The handle asks only for attribute access and
// shares read, write and delete, so it does not conflict with other openers.
func LstatIdentity(path string) (os.FileInfo, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "lstat", Path: path, Err: errors.New("identity handle is unavailable")}
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return nil, errors.Join(statErr, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return info, nil
}
