package recovery

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// systemDiskFullErrors are the errors this system gives for a full disk.
func systemDiskFullErrors() []error {
	return []error{fmt.Errorf("write: %w", windows.ERROR_DISK_FULL), fmt.Errorf("write: %w", windows.ERROR_HANDLE_DISK_FULL)}
}
