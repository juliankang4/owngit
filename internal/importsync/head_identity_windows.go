//go:build windows

package importsync

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func durableFileID(file *os.File) (string, error) {
	handle := windows.Handle(file.Fd())
	var info struct {
		VolumeSerialNumber uint64
		FileID             [16]byte
	}
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileIdInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return "", err
	}
	// A remote protocol must never supply automatic lock-release authority.
	var remote [128]byte
	if err := windows.GetFileInformationByHandleEx(handle, windows.FileRemoteProtocolInfo, (*byte)(unsafe.Pointer(&remote)), uint32(unsafe.Sizeof(remote))); err == nil {
		return "", errors.New("remote filesystem identity is not trusted")
	}
	path, err := windows.GetFinalPathNameByHandle(handle, nil, 0, 0)
	if err != nil || path == 0 {
		return "", errors.New("file volume could not be identified")
	}
	buffer := make([]uint16, path+1)
	if _, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0); err != nil {
		return "", err
	}
	volume := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(&buffer[0], &volume[0], uint32(len(volume))); err != nil {
		return "", err
	}
	if windows.GetDriveType(&volume[0]) != windows.DRIVE_FIXED && windows.GetDriveType(&volume[0]) != windows.DRIVE_RAMDISK {
		return "", errors.New("filesystem does not provide a trusted local file identity")
	}
	filesystem := make([]uint16, 32)
	if err := windows.GetVolumeInformationByHandle(handle, nil, 0, nil, nil, nil, &filesystem[0], uint32(len(filesystem))); err != nil {
		return "", err
	}
	if name := windows.UTF16ToString(filesystem); name != "NTFS" && name != "ReFS" {
		return "", errors.New("filesystem does not provide a trusted local file identity")
	}
	if info.FileID == [16]byte{} {
		return "", errors.New("file identity is unavailable")
	}
	return fmt.Sprintf("windows:%x:%x", info.VolumeSerialNumber, info.FileID), nil
}
