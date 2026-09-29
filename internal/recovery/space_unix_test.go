//go:build !windows

package recovery

import (
	"fmt"
	"syscall"
)

// systemDiskFullErrors are the errors this system gives for a full disk.
func systemDiskFullErrors() []error {
	return []error{fmt.Errorf("write: %w", syscall.ENOSPC)}
}
