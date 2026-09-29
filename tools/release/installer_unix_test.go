//go:build !windows

package main

import (
	"os"
	"syscall"
)

// fileOwner is the user ID that owns the file.
func fileOwner(info os.FileInfo) int {
	return int(info.Sys().(*syscall.Stat_t).Uid)
}
