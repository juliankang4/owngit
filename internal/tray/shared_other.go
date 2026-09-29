//go:build !windows

package tray

import (
	"os"
)

// readSharedFile reads at most limit bytes of the file at path.
func readSharedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file, path, limit)
}
