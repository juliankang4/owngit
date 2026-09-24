//go:build windows

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// hiddenInputSignals end the process while echo is off. Go reports Ctrl+C and
// Ctrl+Break as os.Interrupt, and a closed console, logoff or shutdown as
// SIGTERM.
var hiddenInputSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// disableStdinEcho turns off echo on the stdin console and keeps line input
// and Ctrl+C processing, so a typed secret is read as one line without being
// shown.
func disableStdinEcho() (func() error, error) {
	handle := windows.Handle(os.Stdin.Fd())
	var previous uint32
	if err := windows.GetConsoleMode(handle, &previous); err != nil {
		return nil, err
	}
	hidden := (previous &^ windows.ENABLE_ECHO_INPUT) | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_LINE_INPUT
	if err := windows.SetConsoleMode(handle, hidden); err != nil {
		return nil, err
	}
	return func() error { return windows.SetConsoleMode(handle, previous) }, nil
}
