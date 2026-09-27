//go:build !windows

package state

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// MaximumLinks bounds the symbolic links followed on the way to a path.
const MaximumLinks = 40

// WalkProtected resolves path from "/" one name at a time, following links,
// and calls check for "/" and for every directory, link and file passed
// through before it trusts anything inside. When a name on the way does not
// exist, it returns the resolved directory that should hold it and the
// missing name; otherwise it returns the resolved path.
func WalkProtected(path string, check func(string, os.FileInfo) error) (resolved, missing string, err error) {
	info, err := os.Lstat("/")
	if err != nil {
		return "", "", err
	}
	if err := check("/", info); err != nil {
		return "", "", err
	}
	links := 0
	return walkProtected("/", path, check, &links, true)
}

// walkProtected resolves relative from current, a directory that was already
// resolved and checked. Only the outermost walk may stop at a missing name; a
// link that leads to a missing name is refused.
func walkProtected(current, relative string, check func(string, os.FileInfo) error, links *int, outermost bool) (resolved, missing string, err error) {
	names := strings.Split(relative, "/")
	for index, name := range names {
		switch name {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, name)
		info, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			if !outermost {
				return "", "", fmt.Errorf("a link on the way to %s leads to missing %s", relative, next)
			}
			return current, name, nil
		}
		if err != nil {
			return "", "", err
		}
		if err := check(next, info); err != nil {
			return "", "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			*links++
			if *links > MaximumLinks {
				return "", "", fmt.Errorf("too many links on the way to %s", relative)
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", "", err
			}
			start := current
			if filepath.IsAbs(target) {
				start = "/"
			}
			current, _, err = walkProtected(start, target, check, links, false)
			if err != nil {
				return "", "", err
			}
			continue
		}
		if !info.IsDir() && index < len(names)-1 {
			return "", "", fmt.Errorf("%s is not a directory", next)
		}
		current = next
	}
	return current, "", nil
}

// OthersCanChange reports whether an account other than this one or root
// could change path, or rename or replace what it holds, and a shell command
// that removes that access, or "" when changing the mode does not help. It
// must belong to this account or to root. It must not let others write unless
// it is a sticky directory, where only an entry's owner may rename that
// entry. Group write is accepted only for the account's own private group,
// whose members are treated as the account itself. Access list entries that
// let another account write are refused as well.
func OthersCanChange(path string, info os.FileInfo) (bool, string, error) {
	return othersCanChange(path, info, false, true)
}

// othersCanChange is OthersCanChange; with rootGroups it also accepts group
// write for macOS groups whose members may act as root. allowSticky accepts a
// writable sticky directory when only its existing entries need protection.
func othersCanChange(path string, info os.FileInfo, rootGroups, allowSticky bool) (bool, string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, "", errors.New("owner is unavailable")
	}
	if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
		return true, "", nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, "", nil
	}
	permissions := info.Mode().Perm()
	adminGroup := runtime.GOOS == "darwin" && (stat.Gid == 0 || stat.Gid == 80)
	if (!allowSticky || info.Mode()&os.ModeSticky == 0) && (permissions&0o002 != 0 ||
		permissions&0o020 != 0 && !OwnPrivateGroup(stat.Gid) && !(rootGroups && adminGroup)) {
		return true, "chmod g-w,o-w " + shellQuote(path), nil
	}
	fix, err := accessListFix(path, info)
	return fix != "", fix, err
}

// RequireProtectedPath refuses path when another account could change it or
// anything on the way to it; see WalkProtected and OthersCanChange. On macOS
// it accepts group write for wheel and admin, like Homebrew's folders. A
// writable sticky directory is accepted only as an ancestor, not as path.
func RequireProtectedPath(path string) error { return requireProtectedPath(path, true) }

// RequireProtectedParent applies the ancestor rule to the parent of path.
func RequireProtectedParent(path string) error {
	return requireProtectedPath(filepath.Dir(path), false)
}

func requireProtectedPath(path string, strictFinal bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	check := func(allowSticky bool) func(string, os.FileInfo) error {
		return func(name string, info os.FileInfo) error {
			changeable, fix, err := othersCanChange(name, info, true, allowSticky)
			if err == nil && changeable {
				err = fmt.Errorf("another account can change %s", name)
				if fix != "" {
					err = fmt.Errorf("%w (%s fixes that)", err, fix)
				}
			}
			return err
		}
	}
	resolved, missing, err := WalkProtected(absolute, check(true))
	if err == nil && missing != "" {
		return fmt.Errorf("%s does not exist", path)
	}
	if err == nil && strictFinal {
		info, statErr := os.Lstat(resolved)
		if statErr != nil {
			return statErr
		}
		err = check(false)(resolved, info)
	}
	return err
}

// OwnPrivateGroup reports whether gid is the private group of a runner that
// is not root: the account's primary and effective group, named like the
// account. Many Linux systems give each user such a group and a umask of 002,
// so group write access there is the user's own. A failed lookup counts as a
// shared group.
func OwnPrivateGroup(gid uint32) bool {
	if os.Geteuid() == 0 || int(gid) != os.Getegid() {
		return false
	}
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || account.Gid != strconv.FormatUint(uint64(gid), 10) {
		return false
	}
	group, err := user.LookupGroupId(account.Gid)
	return err == nil && group.Name == account.Username
}
