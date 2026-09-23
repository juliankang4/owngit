package checksource

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// Destination privacy contract.
//
// The destination is created empty, then given owner-only access that is
// applied and verified before any file or directory content is written. The
// order matters: no blob byte and no path component exists while the directory
// still carries whatever access its parent implied.
//
// This is fail-closed. If protection or its verification fails, Materialize
// returns the error and writes nothing further. The empty or partially created
// destination is left for the caller to remove, which is the same ownership
// rule as any other failure after creation.
//
// It also assumes a trusted parent. Between the creating syscall and the
// verified protection, the new directory may still be reachable under the
// parent's access rules. A user who can write the parent directory could plant
// an entry or replace the directory in that window. The parent is therefore
// expected to be a task-owned area that untrusted users cannot write.
// Two checks narrow that window rather than relying on the assumption alone:
// protection verifies that the current process owns the object, and the
// directory is confirmed empty afterwards, so anything planted in the window
// fails the export instead of surviving inside a private-looking result.
//
// None of this makes the destination a sandbox. A process running as the same
// operating-system user retains full access.

// privateDirectoryMode is the Unix permission requested at creation. The state
// package owns the authoritative private-mode policy; this value only avoids a
// group- or world-readable moment before that policy is applied.
const privateDirectoryMode fs.FileMode = 0o700

// protectDestinationHook is the platform implementation. Tests replace it to
// exercise the fail-closed path without changing real access control.
var protectDestinationHook = protectDestination

// createPrivateDestination creates destination as a new private directory and
// returns a confined root for it. The directory is owner-only and verified
// before the caller writes anything into it.
func createPrivateDestination(destination string) (*os.Root, error) {
	if err := os.Mkdir(destination, privateDirectoryMode); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrDestinationExists, destination)
		}
		return nil, fmt.Errorf("create materialization destination: %w", err)
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return nil, fmt.Errorf("confine materialization destination: %w", err)
	}
	if err := protectDestinationHook(root, destination); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("protect materialization destination: %w", err)
	}
	if err := confirmEmptyDestination(root); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

// confirmEmptyDestination fails the export if anything already exists inside
// the freshly protected directory. Reaching this point means the directory was
// created by this call, so any entry was planted before protection took effect.
func confirmEmptyDestination(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("inspect materialization destination: %w", err)
	}
	// Readdirnames with a positive count reports io.EOF for an empty directory,
	// which is the expected result here.
	names, readErr := directory.Readdirnames(1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.Join(fmt.Errorf("list materialization destination: %w", readErr), closeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close materialization destination: %w", closeErr)
	}
	if len(names) > 0 {
		return &EntryError{Path: names[0], Err: ErrPathConflict,
			Detail: "the destination was not empty when its private access was established"}
	}
	return nil
}
