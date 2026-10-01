//go:build !windows

package importsync

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"syscall"
)

// durableFileID is used only on known local filesystems. Unknown filesystems
// require owner recovery instead of trusting potentially synthetic identities.
func durableFileID(file *os.File) (string, error) {
	var fs syscall.Statfs_t
	if err := syscall.Fstatfs(int(file.Fd()), &fs); err != nil {
		return "", err
	}
	local := false
	// Darwin reports a filesystem name; Linux reports a magic number.
	name := reflect.ValueOf(fs).FieldByName("Fstypename")
	if name.IsValid() {
		var bytes []byte
		for i := 0; i < name.Len() && name.Index(i).Int() != 0; i++ {
			bytes = append(bytes, byte(name.Index(i).Int()))
		}
		switch string(bytes) {
		case "apfs", "hfs", "ufs", "tmpfs":
			local = true
		}
	} else {
		switch uint64(fs.Type) {
		case 0xef53, 0x58465342, 0x9123683e, 0x1021994, 0x2fc12fc1:
			local = true
		}
	}
	if !local {
		return "", errors.New("filesystem does not provide a trusted local file identity")
	}
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok || identity.Ino == 0 {
		return "", errors.New("file identity is unavailable")
	}
	return fmt.Sprintf("unix:%x:%x", uint64(identity.Dev), uint64(identity.Ino)), nil
}
