package recovery

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// companionLimit is the largest companion file accepted; those macOS
// writes for OwnGit's files hold a few attributes and take 4 KiB.
const companionLimit = 1 << 20

// appleDoubleMagic starts every AppleDouble file.
var appleDoubleMagic = []byte{0x00, 0x05, 0x16, 0x07}

// isCompanion reports whether name, a path in dir, is the file system's
// companion of the entry base in the same folder, for which exists
// reports. macOS keeps the extended attributes of a file on a volume that
// cannot hold them, such as exFAT or FAT, in a companion file named "._"
// and the file's name (AppleDouble). A companion is the file system's, not
// OwnGit's: it moves and goes with its file, and a backup ignores it. Only
// what macOS writes is one: a regular file, not a link, of at most
// companionLimit bytes that starts with the AppleDouble magic number, beside
// its file. Anything else named so is an unknown entry. This recognizes the
// format; it proves nothing about who wrote the file.
func isCompanion(dir *os.Root, name string, exists func(base string) bool) (bool, error) {
	base, found := strings.CutPrefix(path.Base(name), "._")
	if !found || base == "" || !exists(base) {
		return false, nil
	}
	named, err := dir.Lstat(name)
	if err != nil {
		return false, err
	}
	if !named.Mode().IsRegular() || named.Size() > companionLimit {
		return false, nil
	}
	file, err := dir.Open(name)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(named, opened) {
		return false, fmt.Errorf("%s changed while it was inspected", name)
	}
	magic := make([]byte, len(appleDoubleMagic))
	if _, err := io.ReadFull(file, magic); err == io.EOF || err == io.ErrUnexpectedEOF {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return bytes.Equal(magic, appleDoubleMagic), nil
}
