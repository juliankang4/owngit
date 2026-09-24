//go:build darwin || linux

package main

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// hiddenInputSignals end the process while echo is off: Ctrl+C, Ctrl+\ (both
// keep working because ISIG stays on), kill, and a closed terminal.
var hiddenInputSignals = []os.Signal{os.Interrupt, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP}

// disableStdinEcho turns off echo on the stdin terminal and keeps line editing
// and signals, so a typed secret is read as one line without being shown.
func disableStdinEcho() (func() error, error) {
	fd := int(os.Stdin.Fd())
	previous, err := unix.IoctlGetTermios(fd, getTermiosRequest)
	if err != nil {
		return nil, err
	}
	hidden := *previous
	hidden.Lflag &^= unix.ECHO
	hidden.Lflag |= unix.ICANON | unix.ISIG
	hidden.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(fd, setTermiosRequest, &hidden); err != nil {
		return nil, err
	}
	return func() error { return unix.IoctlSetTermios(fd, setTermiosRequest, previous) }, nil
}
