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
	if (!allowSticky || info.Mode()&os.ModeSticky == 0) && (permissions&0o002 != 0 ||
		permissions&0o020 != 0 && !OwnPrivateGroup(stat.Gid) && !(rootGroups && rootEquivalentGroup(stat.Gid))) {
		return true, "chmod g-w,o-w " + shellQuote(path), nil
	}
	fix, err := accessListFix(path, info)
	return fix != "", fix, err
}

// RequireProtectedPath refuses path when another account could change it or
// anything on the way to it; see WalkProtected and OthersCanChange. On macOS
// it accepts group write for wheel and admin, like Homebrew's folders. A
// writable sticky directory is accepted only as an ancestor, not as path.
func RequireProtectedPath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	resolved, missing, err := WalkProtected(absolute, protectedCheck(true))
	if err == nil && missing != "" {
		return fmt.Errorf("%s does not exist", path)
	}
	if err != nil {
		return err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return err
	}
	return protectedCheck(false)(resolved, info)
}

// rootEquivalentGroup reports whether gid is a macOS group whose members may
// act as root: wheel and admin, which own Homebrew's and the system's folders.
func rootEquivalentGroup(gid uint32) bool {
	return runtime.GOOS == "darwin" && (gid == 0 || gid == 80)
}

// protectedCheck is the WalkProtected check of RequireProtectedPath: it
// refuses a path that another account can change, with the command that
// fixes it when there is one.
func protectedCheck(allowSticky bool) func(string, os.FileInfo) error {
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

// RequireStateParent refuses the absolute path of a state directory, or of
// another state destination such as a restore target, when another account
// could change a folder on the way to it. Before anything is created, names that do not
// exist yet are accepted when the folder that receives them, the last
// existing one, is one that no other account can write: a sticky folder that
// every account may write, such as /tmp, keeps others from renaming what is
// in it, but not from taking a name that does not exist yet. The existing
// folders above cannot be renamed by others either, so nobody else can put
// anything where the missing folders are created. When root runs the
// command, a folder that another account owns belongs to that account, and
// so would state inside it; state that root created there would lock that
// account out. The refusal then says to run the command as that account.
func RequireStateParent(path string) error {
	protected := protectedCheck(true)
	resolved, missing, err := WalkProtected(filepath.Dir(path), func(name string, info os.FileInfo) error {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 && stat.Uid != 0 {
			return &OtherAccountError{Path: name, Account: accountName(stat.Uid)}
		}
		return protected(name, info)
	})
	if _, statErr := os.Lstat(path); err == nil && (missing != "" || statErr != nil) {
		var info os.FileInfo
		if info, err = os.Lstat(resolved); err == nil {
			var changeable bool
			if changeable, _, err = othersCanChange(resolved, info, true, false); err == nil && changeable {
				err = fmt.Errorf("other accounts can create names in %s, where the missing folders would be created", resolved)
			}
		}
	}
	if other := new(OtherAccountError); err != nil && !errors.As(err, &other) {
		return fmt.Errorf("state directory parent is not protected: %w; choose a parent that other accounts cannot change", err)
	}
	return err
}

// checkStateDirectory checks the state directory path after it was created:
// the way to where it leads, once more, now that every folder exists, and
// that the directory belongs to this account. It returns the resolved path.
func checkStateDirectory(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve state directory identity: %w", err)
	}
	if err := RequireStateParent(resolved); err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect state directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !ok:
		return "", errors.New("state directory owner is unavailable")
	case int(stat.Uid) == os.Geteuid():
		return resolved, nil
	case os.Geteuid() == 0:
		return "", &OtherAccountError{Path: resolved, Account: accountName(stat.Uid)}
	}
	return "", errNotStateOwner
}

// accountName is the name of the account uid, or its number when it has no
// name.
func accountName(uid uint32) string {
	if account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil {
		return account.Username
	}
	return strconv.FormatUint(uint64(uid), 10)
}

var errNotRootOnly = errors.New("an account other than root can change it")

// OnlyRootCanChange reports whether root is the only account that can create,
// rename or replace anything on the way to the absolute path: "/" and every
// existing folder and link on the way belong to root, and no other account
// can write to one of them, not even to a sticky folder. Only then can a
// folder that root creates at path for another account, with a command
// OwnGit suggests, not be redirected by a link that another account put
// there first. Names that do not exist yet are fine, because root creates
// them. A path that cannot be checked counts as changeable.
func OnlyRootCanChange(path string) bool {
	_, _, err := WalkProtected(path, func(name string, info os.FileInfo) error {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return errNotRootOnly
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		permissions := info.Mode().Perm()
		if permissions&0o002 != 0 || permissions&0o020 != 0 && !rootEquivalentGroup(stat.Gid) {
			return errNotRootOnly
		}
		if fix, err := accessListFix(name, info); err != nil || fix != "" {
			return errNotRootOnly
		}
		return nil
	})
	return err == nil
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
