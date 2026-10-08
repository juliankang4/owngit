//go:build !windows

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

const temporaryEnvironment = "TMPDIR"

func systemTemporaryRoot() (string, error) { return "/tmp", nil }

func openInspectionRoot(root string, purpose inspectionPurpose) (*os.File, func(), error) {
	var dir *os.File
	var err error
	if purpose == inspectForReader {
		dir, err = openDirectory(root, false, temporaryFolderPolicy)
		err = temporaryRootError(err)
	} else {
		dir, err = openFolder(root)
	}
	return dir, func() {}, err
}

func temporaryRootError(err error) error {
	var private *NotPrivateError
	if errors.As(err, &private) {
		return fmt.Errorf("unsafe temporary directory: %s", err)
	}
	return err
}

func protectInspectionStage(stage *os.File) error {
	readable, err := openReadableStateDirectory(stage)
	if err != nil {
		return err
	}
	return closeAfter(readable, ProtectPrivateHandle(readable, true))
}

func createPrivateFileIn(parent *os.File, name string) (*os.File, error) {
	path := filepath.Join(parent.Name(), name)
	descriptor, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	runtime.KeepAlive(parent)
	if err != nil {
		return nil, &os.PathError{Op: "create private file", Path: path, Err: err}
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

// protectionFingerprint describes the permission state that a refusal must
// leave unchanged. It is compared as an opaque string.
func protectionFingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("mode=%04o", info.Mode().Perm()), nil
}

// lookupSourceEntry and openSourceEntry use the entry's path. Removing a file
// on Unix removes its name at once, so an entry is either present or absent;
// the held directory is not needed to tell.
func lookupSourceEntry(_ *os.File, path string) (os.FileInfo, error) {
	return LstatIdentity(path)
}

func openSourceEntry(_ *os.File, path string, metadataOnly bool) (*os.File, error) {
	return openSourceHandle(path, metadataOnly)
}

func openSourceHandle(path string, _ bool) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open protected state entry", Path: path, Err: err}
	}
	return os.NewFile(uintptr(descriptor), path), nil
}
