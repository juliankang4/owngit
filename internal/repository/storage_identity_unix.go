//go:build !windows

package repository

import "os"

func directRepositoryDirectory(info os.FileInfo) bool {
	return info.IsDir() && info.Mode()&os.ModeSymlink == 0
}
