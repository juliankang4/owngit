//go:build !windows

package checksource

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"owngit/internal/state"
)

// maximumAncestorLinks bounds the symbolic links followed while checking the
// directories above a workspace root.
const maximumAncestorLinks = 40

// openWorkspaceDirectory opens the root itself, never a link in its place.
func openWorkspaceDirectory(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
}

// protectWorkspaceDirectory changes the mode through the held handle, so a
// path replacement cannot redirect it.
func protectWorkspaceDirectory(directory *os.File, _ string) error {
	return state.ProtectPrivateHandle(directory, true)
}

// requireProtectedAncestors refuses a root when another account could rename
// or replace a directory on the way to it. Every directory passed through,
// including the targets of symbolic links, must belong to this account or to
// root and must not be writable by others or by a shared group unless it is
// sticky: in a sticky directory only an entry's owner may rename that entry. The root's
// path then keeps naming the same directory, so the path-based marker, lock,
// job and command steps that follow stay inside it. A missing tail is allowed
// because only this account or root can create it in such a directory.
func requireProtectedAncestors(root string) error {
	links := 0
	_, _, err := walkProtectedAncestors("/", filepath.Dir(root), root, &links)
	return err
}

// walkProtectedAncestors resolves relative from current, a directory that was
// already resolved and checked, and returns the resolved directory. missing
// reports that resolution stopped at a component that does not exist yet.
func walkProtectedAncestors(current, relative, root string, links *int) (resolved string, missing bool, err error) {
	if current == "/" {
		info, err := os.Lstat("/")
		if err != nil {
			return "", false, fmt.Errorf("inspect check workspace parent: %w", err)
		}
		if err := requireProtectedAncestor("/", info, root); err != nil {
			return "", false, err
		}
	}
	for _, name := range strings.Split(relative, "/") {
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
			return next, true, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("inspect check workspace parent: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			*links++
			if *links > maximumAncestorLinks {
				return "", false, errors.New("too many links above the check workspace root")
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", false, fmt.Errorf("inspect check workspace parent: %w", err)
			}
			start := current
			if filepath.IsAbs(target) {
				start = "/"
			}
			current, missing, err = walkProtectedAncestors(start, target, root, links)
			if err != nil || missing {
				return current, missing, err
			}
			continue
		}
		if !info.IsDir() {
			return "", false, fmt.Errorf("check workspace parent %s is not a directory", next)
		}
		if err := requireProtectedAncestor(next, info, root); err != nil {
			return "", false, err
		}
		current = next
	}
	return current, false, nil
}

func requireProtectedAncestor(path string, info os.FileInfo, root string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("check workspace parent owner is unavailable")
	}
	if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
		return &UnsafeWorkspaceRootError{Root: root, Directory: path}
	}
	permissions := info.Mode().Perm()
	if info.Mode()&os.ModeSticky != 0 || permissions&0o022 == 0 ||
		(permissions&0o002 == 0 && ownPrivateGroup(stat.Gid)) {
		return nil
	}
	return &UnsafeWorkspaceRootError{
		Root: root, Directory: path,
		Fix: "chmod g-w,o-w '" + strings.ReplaceAll(path, "'", `'\''`) + "'",
	}
}

// ownPrivateGroup reports whether gid is this account's own group: its
// effective group, named like the account. Many Linux systems give each user
// such a group and a umask of 002, so group write access there is the user's
// own. A failed lookup counts as a shared group.
func ownPrivateGroup(gid uint32) bool {
	if int(gid) != os.Getegid() {
		return false
	}
	account, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		return false
	}
	group, err := user.LookupGroupId(strconv.FormatUint(uint64(gid), 10))
	return err == nil && group.Name == account.Username
}
