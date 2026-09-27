//go:build !windows

package main

import (
	"context"
	"errors"
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
func platformSystemDirectory() (string, error)               { return "", errNotWindows }
func attachToConsole(int)                                    {}
func platformOwnerOf(string) (string, error)                 { return "", errNotWindows }
func platformRepositoryRootWithoutAdminRights(string) string { return "" }
func platformServiceInstallPaths() (serviceInstallPaths, error) {
	return serviceInstallPaths{
		Directory:  `C:\Program Files\OwnGit`,
		Executable: `C:\Program Files\OwnGit\owngit.exe`,
		Temp:       `C:\Program Files\OwnGit\temp`,
	}, nil
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
func platformRunAttachedWithEnvironment([]string, string, ...string) error { return errNotWindows }

func platformGiveOwnership(string, string, func(string, string) error) (int, int, error) {
	return 0, 0, errNotWindows
}
func watchServiceStop(string, func(), func(string, ...any)) func() { return func() {} }

// serveWithoutAdminRights applies to Windows only.
func serveWithoutAdminRights([]string) (bool, error) { return false, nil }
