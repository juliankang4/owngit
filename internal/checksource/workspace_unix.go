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

// beforeCreatingWorkspaceParent lets a test act as another account that
// creates a missing directory between the check and its creation.
var beforeCreatingWorkspaceParent = func(string) {}

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

// requirePrivateWorkspaceACL refuses a root whose access list lets another
// account change it. The owner-only mode set afterwards does not remove every
// kind of access list entry.
func requirePrivateWorkspaceACL(path string) error {
	fix, err := rootACLFix(path)
	if err != nil {
		return fmt.Errorf("inspect check workspace root access list: %w", err)
	}
	if fix != "" {
		return &UnsafeWorkspaceRootError{Root: path, Directory: path, Fix: fix}
	}
	return nil
}

// prepareWorkspaceRoot checks the directories above root and creates the
// missing ones and root itself one at a time. Each directory is made inside a
// directory that was already checked, then opened without following links and
// checked through that handle before anything is made inside it, so another
// account that creates a missing name first is found instead of trusted.
func prepareWorkspaceRoot(root string) error {
	limit := strings.Count(root, "/") + maximumAncestorLinks + 1
	for range limit {
		parent, missing, err := resolveProtectedParent(root)
		if err != nil {
			return err
		}
		if missing == "" {
			err := os.Mkdir(filepath.Join(parent, filepath.Base(root)), 0o700)
			if err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create check workspace root: %w", err)
			}
			return nil
		}
		if err := createProtectedAncestor(filepath.Join(parent, missing), root); err != nil {
			return err
		}
	}
	return errors.New("check workspace root parents keep changing")
}

// createProtectedAncestor makes one missing directory above root, or accepts
// the one that appeared under that name, when it passes the ancestor rules.
func createProtectedAncestor(path, root string) error {
	beforeCreatingWorkspaceParent(path)
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create check workspace parent: %w", err)
	}
	directory, err := openWorkspaceDirectory(path)
	if err != nil {
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			// The next walk checks the link and where it leads.
			return nil
		}
		return fmt.Errorf("open check workspace parent: %w", err)
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return fmt.Errorf("inspect check workspace parent: %w", err)
	}
	return requireProtectedAncestor(path, info, root)
}

// requireProtectedAncestors refuses a root when another account could rename
// or replace a directory on the way to it; see requireProtectedAncestor. The
// root's path then keeps naming the same directory, so the path-based marker,
// lock, job and command steps that follow stay inside it.
func requireProtectedAncestors(root string) error {
	_, missing, err := resolveProtectedParent(root)
	if err == nil && missing != "" {
		err = fmt.Errorf("check workspace parent %s does not exist", missing)
	}
	return err
}

// resolveProtectedParent checks every directory and link passed through on
// the way to root's parent and returns the parent's resolved path. When a
// directory on the way does not exist yet, it returns the resolved directory
// that should hold it and the missing name.
func resolveProtectedParent(root string) (parent, missing string, err error) {
	info, err := os.Lstat("/")
	if err != nil {
		return "", "", fmt.Errorf("inspect check workspace parent: %w", err)
	}
	if err := requireProtectedAncestor("/", info, root); err != nil {
		return "", "", err
	}
	links := 0
	return walkProtectedAncestors("/", filepath.Dir(root), root, &links, true)
}

// walkProtectedAncestors resolves relative from current, a directory that was
// already resolved and checked. Only the outermost walk may stop at a missing
// name; a link that leads to a missing directory is refused.
func walkProtectedAncestors(current, relative, root string, links *int, outermost bool) (resolved, missing string, err error) {
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
			if !outermost {
				return "", "", fmt.Errorf("a link above check workspace root %s leads to missing %s", root, next)
			}
			return current, name, nil
		}
		if err != nil {
			return "", "", fmt.Errorf("inspect check workspace parent: %w", err)
		}
		if err := requireProtectedAncestor(next, info, root); err != nil {
			return "", "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			*links++
			if *links > maximumAncestorLinks {
				return "", "", errors.New("too many links above the check workspace root")
			}
			target, err := os.Readlink(next)
			if err != nil {
				return "", "", fmt.Errorf("inspect check workspace parent: %w", err)
			}
			start := current
			if filepath.IsAbs(target) {
				start = "/"
			}
			current, _, err = walkProtectedAncestors(start, target, root, links, false)
			if err != nil {
				return "", "", err
			}
			continue
		}
		if !info.IsDir() {
			return "", "", fmt.Errorf("check workspace parent %s is not a directory", next)
		}
		current = next
	}
	return current, "", nil
}

// requireProtectedAncestor refuses a directory or link above a workspace root
// that another account could use to rename or replace what it holds. It must
// belong to this account or to root. A directory must not let others write
// unless it is sticky, where only an entry's owner may rename that entry.
// Group write is refused for a runner started as root. For another account it
// is accepted only for that account's own private group, whose members are
// treated as the account itself. Access list entries that let another account
// write are refused as well.
func requireProtectedAncestor(path string, info os.FileInfo, root string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("check workspace parent owner is unavailable")
	}
	if stat.Uid != 0 && int(stat.Uid) != os.Geteuid() {
		return &UnsafeWorkspaceRootError{Root: root, Directory: path}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	modeFix := "chmod g-w,o-w " + quoteWorkspacePath(path)
	permissions := info.Mode().Perm()
	if info.Mode()&os.ModeSticky == 0 && (permissions&0o002 != 0 ||
		permissions&0o020 != 0 && !ownPrivateGroup(stat.Gid)) {
		return &UnsafeWorkspaceRootError{Root: root, Directory: path, Fix: modeFix}
	}
	fix, err := ancestorACLFix(path, info)
	if err != nil {
		return fmt.Errorf("inspect check workspace parent access list: %w", err)
	}
	if fix != "" {
		return &UnsafeWorkspaceRootError{Root: root, Directory: path, Fix: fix}
	}
	return nil
}

// ownPrivateGroup reports whether gid is the private group of a runner that
// is not root: the account's primary and effective group, named like the
// account. Many Linux systems give each user such a group and a umask of 002,
// so group write access there is the user's own. A failed lookup counts as a
// shared group.
func ownPrivateGroup(gid uint32) bool {
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

// quoteWorkspacePath quotes a path for a POSIX shell command in a message.
func quoteWorkspacePath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}
