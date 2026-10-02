//go:build !darwin && !linux

package state

import "os"

// ExposedToOtherAccounts keeps the existing folder classification on Windows,
// where DACL inspection already covers inherited access, and on platforms
// without an access-list reader.
func ExposedToOtherAccounts(_ string, file *os.File, info os.FileInfo) (bool, error) {
	return OthersCanChangeFile(file, info)
}
