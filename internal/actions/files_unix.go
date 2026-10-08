//go:build !windows

package actions

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func commandReadFlags() int { return os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK }

func singleLink(file *os.File, _ os.FileInfo) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("file must have one link")
	}
	return nil
}
