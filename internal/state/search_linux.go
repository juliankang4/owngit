//go:build linux

package state

import "golang.org/x/sys/unix"

// searchOnly opens a directory only to look up names in it, which needs no
// permission to list it.
const searchOnly = unix.O_PATH
