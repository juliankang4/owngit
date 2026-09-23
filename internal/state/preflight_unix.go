//go:build !windows

package state

import (
	"fmt"
	"os"
)

// protectionFingerprint describes the permission state that a refusal must
// leave unchanged. It is compared as an opaque string.
func protectionFingerprint(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("mode=%04o", info.Mode().Perm()), nil
}

// openSourceHandle opens a source entry read-only. The caller compares the
// handle identity with the directory entry, so no data is read from a handle
// that is only used for identity.
func openSourceHandle(path string, _ bool) (*os.File, error) {
	return os.Open(path)
}
