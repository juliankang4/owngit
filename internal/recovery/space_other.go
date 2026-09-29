//go:build !darwin && !linux && !windows

package recovery

import (
	"errors"
	"syscall"
)

// diskFreeSpace cannot tell the free space here; restore then relies on the
// error of a full disk.
func diskFreeSpace(string) (uint64, bool, error) { return 0, false, nil }

func diskFullError(err error) bool { return errors.Is(err, syscall.ENOSPC) }
