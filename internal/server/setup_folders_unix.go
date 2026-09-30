//go:build !windows

package server

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
)

func chooserHidden(_, name string) (bool, error) { return strings.HasPrefix(name, "."), nil }
func platformFolderName(name string) bool        { return len(name) <= 255 }
func folderNotDirectory(err error) bool          { return errors.Is(err, syscall.ENOTDIR) }
func folderLinkLoop(err error) bool              { return errors.Is(err, syscall.ELOOP) }
func folderParent(path string) (string, bool) {
	parent := filepath.Dir(path)
	if parent == path {
		return "", false
	}
	return parent, false
}
func folderRoots(context.Context) (folderResult, error) { return folderResult{}, errFolderPath }
