//go:build !linux

package service

import "os"

// DropTo is only needed where the systemd account mode exists.
func DropTo(uid, gid int) error { return ErrUnsupported }

// FileOwner is only needed where the systemd account mode exists.
func FileOwner(os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
