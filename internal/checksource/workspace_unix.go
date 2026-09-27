//go:build !windows

package checksource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"owngit/internal/state"
)

// beforeCreatingWorkspaceParent lets a test act as another account that
// creates a missing directory between the check and its creation.
var beforeCreatingWorkspaceParent = func(string) {}

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
	fix, err := state.ChangeAccessListFix(path)
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
	limit := strings.Count(root, "/") + state.MaximumLinks + 1
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
	return state.WalkProtected(filepath.Dir(root), func(path string, info os.FileInfo) error {
		return requireProtectedAncestor(path, info, root)
	})
}

// requireProtectedAncestor refuses a directory or link above a workspace root
// that another account could use to rename or replace what it holds; see
// state.OthersCanChange.
func requireProtectedAncestor(path string, info os.FileInfo, root string) error {
	changeable, fix, err := state.OthersCanChange(path, info)
	if err != nil {
		return fmt.Errorf("inspect check workspace parent: %w", err)
	}
	if changeable {
		return &UnsafeWorkspaceRootError{Root: root, Directory: path, Fix: fix}
	}
	return nil
}
