//go:build linux

package service

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// DropTo makes this process run as the account uid, with its primary group
// gid and its supplementary groups, for good. Go applies the change to
// every thread of the process.
func DropTo(uid, gid int) error {
	groups := []int{gid}
	if account, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		if ids, err := account.GroupIds(); err == nil {
			for _, id := range ids {
				if value, err := strconv.Atoi(id); err == nil && value != gid {
					groups = append(groups, value)
				}
			}
		}
	}
	if err := syscall.Setgroups(groups); err != nil {
		return fmt.Errorf("set groups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("set group: %w", err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("set user: %w", err)
	}
	return nil
}

// FileOwner returns the owner and group of a file.
func FileOwner(info os.FileInfo) (uid, gid int, ok bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(stat.Uid), int(stat.Gid), true
}
