//go:build darwin || linux

package recovery

import (
	"errors"
	"fmt"
	"os"
)

// renameDirectoryOnce publishes the directory oldPath where the file system
// cannot rename without replacing (unsupported is its refusal). A rename of
// a directory replaces nothing but an empty directory: a file, a link or a
// folder with content that appears at newPath after the check below makes
// it fail. So nothing that holds data is replaced here either.
func renameDirectoryOnce(oldPath, newPath string, unsupported error) error {
	info, err := os.Lstat(oldPath)
	if err != nil || !info.IsDir() {
		return unsupported
	}
	if _, err := os.Lstat(newPath); err == nil {
		return fmt.Errorf("%s already exists", newPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(oldPath, newPath)
}
