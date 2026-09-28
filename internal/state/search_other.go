//go:build !linux && !windows

package state

import "golang.org/x/sys/unix"

// searchOnly opens a directory to look up names in it. Outside Linux that
// needs permission to read it.
const searchOnly = unix.O_RDONLY
