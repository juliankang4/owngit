//go:build !darwin && !windows

package state

import "os"

// MkdirPrivate creates a mode-0700 directory. On Linux, a default POSIX ACL is
// masked by the requested mode at creation, so it grants no group access.
func MkdirPrivate(path string) error {
	return privateDirectoryMkdirError(path, os.Mkdir(path, 0o700))
}
