//go:build darwin

package state

import "golang.org/x/sys/unix"

func renameExclusive(oldPath, newPath string) error {
	return unix.RenamexNp(oldPath, newPath, unix.RENAME_EXCL)
}
