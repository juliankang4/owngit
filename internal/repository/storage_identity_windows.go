package repository

import (
	"os"
	"syscall"
)

func directRepositoryDirectory(info os.FileInfo) bool {
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && info.IsDir() && attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT == 0
}
