//go:build !windows

package state

import "os"

// LstatIdentity inspects path like os.Lstat, without following a final link,
// and records the file identity at the time of the call. A Unix Lstat result
// already carries the device and inode, so this is os.Lstat. The Windows
// version explains why a separate helper is needed there.
func LstatIdentity(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}
