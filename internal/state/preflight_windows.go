//go:build windows

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

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

// sourceAccess is the access of a source handle. A metadata-only handle
// requests no data access.
func sourceAccess(metadataOnly bool) uint32 {
	access := uint32(windows.GENERIC_READ)
	if metadataOnly {
		access = windows.FILE_READ_ATTRIBUTES
	}
	return access | windows.READ_CONTROL | windows.WRITE_DAC | windows.WRITE_OWNER
}

// openSourceHandle opens the state directory without following a reparse
// point and with delete sharing. Its entries are opened through it with
// openSourceEntry.
func openSourceHandle(path string, metadataOnly bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	handle, err := windows.CreateFile(name, sourceAccess(metadataOnly),
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
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

// lookupSourceEntry records the identity of the entry path, a direct child of
// the held directory dir, like LstatIdentity.
func lookupSourceEntry(dir *os.File, path string) (os.FileInfo, error) {
	file, err := openEntry(dir, path, windows.FILE_READ_ATTRIBUTES, "lstat")
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	if closeErr := file.Close(); statErr != nil || closeErr != nil {
		return nil, errors.Join(statErr, closeErr)
	}
	return info, nil
}

// openSourceEntry opens the entry path, a direct child of the held directory
// dir, with delete sharing, so a live SQLite owner can still remove its
// sidecars while the inspection holds identity.
func openSourceEntry(dir *os.File, path string, metadataOnly bool) (*os.File, error) {
	return openEntry(dir, path, sourceAccess(metadataOnly), "open")
}

// errDeletePending is the cause of an entry whose deletion is pending. The
// deletion can still be cancelled by whoever holds the entry open, so the
// entry is neither present nor absent yet: the directory is changing, and the
// caller retries once the deletion has finished or been cancelled.
var errDeletePending = fmt.Errorf("%w: its deletion is pending", ErrInspectionUnstable)

// openEntry opens a direct child of dir by name, without following a reparse
// point, through NtCreateFile. When SQLite deletes a sidecar that another
// process still holds open, for example another opener's inspection, the
// name stays until the last handle closes, and every open by name fails.
// CreateFile reports that as access denied, the same as a file this account
// may not open. NtCreateFile reports STATUS_DELETE_PENDING instead, which is
// a change in progress (errDeletePending), not a failure. A name relative to
// the held directory needs no conversion to an NT path.
func openEntry(dir *os.File, path string, access uint32, op string) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(filepath.Base(path))
	if err != nil {
		return nil, &os.PathError{Op: op, Path: path, Err: err}
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(dir.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	status := windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE, &attributes, &windows.IO_STATUS_BLOCK{}, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	runtime.KeepAlive(dir)
	if status != nil {
		return nil, &os.PathError{Op: op, Path: path, Err: entryOpenError(status)}
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: op, Path: path, Err: errors.New("create source handle")}
	}
	return file, nil
}

// entryOpenError turns the status of a failed NtCreateFile into the error
// that CreateFile would return, except for a pending deletion.
func entryOpenError(status error) error {
	if status == windows.STATUS_DELETE_PENDING {
		return errDeletePending
	}
	if ntStatus, ok := status.(windows.NTStatus); ok {
		return ntStatus.Errno()
	}
	return status
}
