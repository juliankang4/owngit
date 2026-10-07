//go:build !windows

package tray

import (
	"owngit/internal/state"
)

// readSharedFile reads at most limit bytes of the file at path.
func readSharedFile(path string, limit int64) ([]byte, error) {
	file, err := state.OpenPrivateInputFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file, path, limit)
}
