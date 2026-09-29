//go:build !windows

package main

import "golang.org/x/sys/unix"

// canWrite reports whether this account may create and replace files in
// dir.
func canWrite(dir string) bool { return unix.Access(dir, unix.W_OK) == nil }
