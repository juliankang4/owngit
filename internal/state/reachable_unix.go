//go:build darwin || linux

package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ExposedToOtherAccounts reports a changeable folder only when another
// account can also search every folder above it. The target and every folder
// on the way are inspected through held handles, so replacing a path cannot
// redirect the decision. An inspection failure remains an error, not proof
// that the folder is unreachable.
func ExposedToOtherAccounts(path string, file *os.File, info os.FileInfo) (bool, error) {
	changeable, err := OthersCanChangeFile(file, info)
	if err != nil || !changeable {
		return changeable, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	blocked := false
	walked, _, missing, err := walkWayWithOpen(absolute, func(entry wayEntry) error {
		if entry.last || entry.dir == nil {
			return nil
		}
		stat, ok := entry.info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("ancestor owner is unavailable")
		}
		if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
			return nil
		}
		if entry.info.Mode().Perm()&0o001 != 0 {
			return nil
		}
		searchable, err := groupOrAccessListAllowsOtherSearch(entry.dir, entry.info)
		if err != nil {
			return fmt.Errorf("inspect search access to %s: %w", entry.path, err)
		}
		if !searchable {
			blocked = true
		}
		return nil
	}, nil, openReachableDirectoryAt)
	if err != nil {
		return false, err
	}
	defer walked.Close()
	if missing != "" {
		return false, fmt.Errorf("%s changed while its ancestors were checked", path)
	}
	walkedInfo, err := walked.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(walkedInfo, info) {
		return false, fmt.Errorf("%s changed while its ancestors were checked", path)
	}
	return !blocked, nil
}
