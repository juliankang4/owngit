package recovery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// requireAttributesInPlace refuses dir, where a restore would publish the
// repositories, before any work when its file system keeps extended
// attributes in companion files (isCompanion): Git would read those as
// refs and pack files of the restored repositories. It gives a new file
// there an attribute once to find out.
func requireAttributesInPlace(dir string) (err error) {
	suffix, err := randomSuffix()
	if err != nil {
		return err
	}
	name := ".owngit-attribute-check-" + suffix
	probe, companion := filepath.Join(dir, name), filepath.Join(dir, "._"+name)
	file, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	// The probe goes, and its companion with it; a failure to remove
	// either is reported.
	defer func() {
		for _, name := range []string{probe, companion} {
			if removeErr := os.Remove(name); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) && err == nil {
				err = fmt.Errorf("remove the attribute check: %w", removeErr)
			}
		}
	}()
	if err := file.Close(); err != nil {
		return err
	}
	// A file system that stores no attributes makes no companion either.
	if err := unix.Setxattr(probe, "org.owngit.attribute-check", []byte{1}, 0); err != nil && !errors.Is(err, unix.ENOTSUP) {
		return fmt.Errorf("give %s an attribute: %w", probe, err)
	}
	if _, err := os.Lstat(companion); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return &LimitError{Dir: dir, FileSystem: fileSystemName(dir), Reason: "keeps file attributes in separate ._ files, which Git would read as part of the restored repositories; restore the repositories to a folder on another disk"}
}
