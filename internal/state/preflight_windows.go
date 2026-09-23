//go:build windows

package state

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// protectionFingerprint describes the owner and DACL that a refusal must leave
// unchanged. It is compared as an opaque SDDL string.
func protectionFingerprint(path string) (string, error) {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	if descriptor == nil {
		return "", errors.New("no security descriptor")
	}
	text := descriptor.String()
	if text == "" {
		return "", errors.New("security descriptor cannot be encoded")
	}
	return text, nil
}

// openSourceHandle opens a source entry without following a reparse point and
// with delete sharing, so a live SQLite owner can still remove its sidecars
// while the inspection holds identity. A metadata-only handle requests no
// data access.
func openSourceHandle(path string, metadataOnly bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	access := uint32(windows.GENERIC_READ)
	if metadataOnly {
		access = windows.FILE_READ_ATTRIBUTES
	}
	handle, err := windows.CreateFile(name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: errors.New("create source handle")}
	}
	return file, nil
}
