//go:build windows

package recovery

import "os"

func renameNoReplace(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
