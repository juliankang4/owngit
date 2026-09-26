//go:build linux

package service

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// DropTo makes this process run as the account uid, with its primary group
// gid and its supplementary groups, for good. Go applies the change to
// every thread of the process. It first gives up the controlling terminal,
// so that neither this process nor anything it starts as the account can
// open root's terminal or push input into it.
func DropTo(uid, gid int) error {
	if err := DetachTerminal(); err != nil {
		return err
	}
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

// DetachTerminal gives up this process's controlling terminal, if it has
// one. The process keeps its open descriptors, so it can still write to
// the terminal, but /dev/tty no longer opens for it or its children, and
// the terminal no longer accepts injected input (TIOCSTI) from them.
func DetachTerminal() error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	defer tty.Close()
	if leader, err := unix.Getsid(0); err == nil && leader == os.Getpid() {
		// For a session leader the kernel also hangs up the terminal's
		// foreground group, which includes this process.
		if !signal.Ignored(syscall.SIGHUP) {
			signal.Ignore(syscall.SIGHUP)
			defer signal.Reset(syscall.SIGHUP)
		}
	}
	if err := unix.IoctlSetInt(int(tty.Fd()), unix.TIOCNOTTY, 0); err != nil {
		return fmt.Errorf("give up the controlling terminal: %w", err)
	}
	if again, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		again.Close()
		return errors.New("the controlling terminal is still attached")
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
