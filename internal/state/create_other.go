//go:build !darwin && !linux

package state

import "errors"

// renameExclusive is unavailable here. Windows publishes with MoveFileEx, and
// other systems fall back to a hard link.
func renameExclusive(string, string) error {
	return errors.ErrUnsupported
}
