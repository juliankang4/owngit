//go:build windows

package checksource

import (
	"os"

	"owngit/internal/state"
)

func openWorkspaceDirectory(path string) (*os.File, error) {
	return os.Open(path)
}

// protectWorkspaceDirectory replaces the root's owner and access list with the
// current user alone. state.ProtectPrivatePath first refuses an object owned by
// anyone else. A read handle cannot change the security descriptor, so this
// uses the path; the root was already checked to be a real directory.
func protectWorkspaceDirectory(_ *os.File, path string) error {
	return state.ProtectPrivatePath(path, true)
}

// requireProtectedAncestors has no Windows counterpart. Access to rename or
// replace a directory there comes from access lists rather than owner and mode
// bits, and the default runner workspace lies inside the user's own profile.
func requireProtectedAncestors(string) error {
	return nil
}
