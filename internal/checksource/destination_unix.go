//go:build !windows

package checksource

import (
	"os"

	"owngit/internal/state"
)

// protectDestination applies owner-only access through the held root handle,
// so a pathname replacement between creation and protection cannot redirect
// the change to another directory.
//
// On Unix the creating mkdir already sets the permission bits, subject to
// umask. This call removes any umask-dependent difference and reuses the state
// package's single definition of private mode instead of restating it.
// The destination path is unused here because the handle already identifies
// the object; the Windows build needs it.
func protectDestination(root *os.Root, _ string) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	protectErr := state.ProtectPrivateHandle(directory, true)
	closeErr := directory.Close()
	if protectErr != nil {
		return protectErr
	}
	return closeErr
}
