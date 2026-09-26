//go:build !windows

package main

import (
	"errors"
	"os"
	"strconv"
)

// The Windows backend needs Windows; elsewhere its operating system steps
// only exist so that the shared code builds and tests can replace them.

var errNotWindows = errors.New("this needs Windows")

func platformRunElevated([]string) (int, error)                    { return 0, errNotWindows }
func platformRunWithoutAdminRights([]string) (int, error)          { return 0, errNotWindows }
func platformSignalServiceStop(string) (bool, error)               { return false, errNotWindows }
func platformCurrentAccountSID() (string, error)                   { return "S-1-5-21-" + strconv.Itoa(os.Getuid()), nil }
func platformSystemDirectory() (string, error)                     { return "", errNotWindows }
func attachToConsole(int)                                          {}
func watchServiceStop(string, func(), func(string, ...any)) func() { return func() {} }

// serveWithoutAdminRights applies to Windows only.
func serveWithoutAdminRights([]string) (bool, error) { return false, nil }
