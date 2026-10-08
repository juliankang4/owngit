//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

// The Windows backend needs Windows; elsewhere its operating system steps
// only exist so that the shared code builds and tests can replace them.

var errNotWindows = errors.New("this needs Windows")

func platformRunElevated([]string) (int, error)              { return 0, errNotWindows }
func platformRunWithoutAdminRights([]string) (int, error)    { return 0, errNotWindows }
func platformSignalServiceStop(string) (bool, error)         { return false, errNotWindows }
func platformCurrentAccountSID() (string, error)             { return "S-1-5-21-" + strconv.Itoa(os.Getuid()), nil }
func platformRequestingProcess() (string, string, error)     { return "", "", errNotWindows }
func platformAccountProfile(string) (string, error)          { return "", errNotWindows }
func platformIsOwnGitState(string) bool                      { return false }
func platformSystemDirectory() (string, error)               { return "", errNotWindows }
func attachToConsole(int)                                    {}
func platformOwnerOf(string) (string, error)                 { return "", errNotWindows }
func platformRepositoryRootWithoutAdminRights(string) string { return "" }

func platformOwnersRequestOwner(file *os.File) (string, error) {
	return ownerOf(file.Name())
}

func platformConsumeOwnersRequest(path, sid string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > ownersRequestLimit {
		return nil, errors.New("the request is not a plain file")
	}
	if owner, ownerErr := ownersRequestOwner(file); ownerErr != nil || owner != sid {
		return nil, errors.New("the request does not belong to the account that asked")
	}
	content, err := io.ReadAll(io.LimitReader(file, ownersRequestLimit+1))
	if err != nil {
		return nil, err
	}
	if len(content) > ownersRequestLimit {
		return nil, errors.New("the request is not a plain file")
	}
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("remove the request: %w", err)
	}
	return content, nil
}
func platformServiceInstallPaths() (serviceInstallPaths, error) {
	return serviceInstallPaths{
		Directory:  `C:\Program Files\OwnGit`,
		Executable: `C:\Program Files\OwnGit\owngit.exe`,
		Temp:       `C:\Program Files\OwnGit\temp`,
	}, nil
}
func platformPrepareServiceInstall(serviceInstallPaths) (string, error) {
	return "", errNotWindows
}
func platformPrepareServiceStorage(serviceInstallPaths) error      { return errNotWindows }
func platformReplaceServiceCopy(string, serviceInstallPaths) error { return errNotWindows }
func platformTrustedWinget() (string, error)                       { return "", errNotWindows }
func platformServiceEnvironment(serviceInstallPaths, []string) ([]string, error) {
	return nil, errNotWindows
}
func platformApplyServiceEnvironment([]string) error { return errNotWindows }
func platformRunWithEnvironment(context.Context, []string, string, ...string) ([]byte, error) {
	return nil, errNotWindows
}
func platformRunAttachedWithEnvironment([]string, string, ...string) (int, error) {
	return 0, errNotWindows
}
func platformGitOnServicePath() bool                   { return false }
func platformProgramRunning(string) bool               { return false }
func platformReplaceableByOthers(string, string) error { return errNotWindows }

func platformGiveOwnership(string, string, func(string, string) error) (int, int, error) {
	return 0, 0, errNotWindows
}
func watchServiceStop(string, func(), func(string, ...any)) func() { return func() {} }
func watchParentExit(func())                                       {}

// serveWithoutAdminRights applies to Windows only.
func serveWithoutAdminRights([]string) (bool, error) { return false, nil }
