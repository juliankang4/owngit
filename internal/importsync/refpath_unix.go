//go:build !windows

package importsync

import "os"

func refPathIsIndirect(_ string, info os.FileInfo) (bool, error) {
	return info.Mode()&os.ModeSymlink != 0, nil
}
