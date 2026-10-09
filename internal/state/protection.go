package state

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"owngit/internal/statepath"
)

type ProtectionChange struct {
	Path   string `json:"path"`
	Before string `json:"before"`
}

func protectStateObject(root string, file *os.File, directory bool) error {
	if err := requireManagedObject(file, directory); err != nil {
		return err
	}
	before, err := heldProtectionFingerprint(file)
	if err != nil {
		return stateProtectionError(file.Name(), directory, err)
	}
	protectErr := ProtectPrivateHandle(file, directory)
	if protectErr == nil && runtime.GOOS != "windows" {
		info, err := file.Stat()
		mode := os.FileMode(0o600)
		if directory {
			mode = 0o700
		}
		if err == nil && info.Mode().Perm() != mode {
			err = errors.New("private permissions did not remain set")
		}
		protectErr = err
	}
	after, readErr := heldProtectionFingerprint(file)
	if before != after || readErr != nil {
		relative, err := filepath.Rel(root, file.Name())
		if err != nil {
			relative = file.Name()
		}
		if err := errors.Join(protectErr, readErr); err != nil {
			log.Printf("state protection %q: changed %s to %s; verification failed: %v", relative, before, after, err)
		} else {
			log.Printf("state protection %q: changed %s to %s", relative, before, after)
		}
	}
	if err := errors.Join(protectErr, readErr); err != nil {
		return stateProtectionError(file.Name(), directory, err)
	}
	return nil
}

func requireManagedObject(file *os.File, directory bool) error {
	if !directory {
		if err := requireOwnFile(file); err != nil {
			if errors.Is(err, errMultipleFileNames) {
				return stateEntryRepairError(file.Name(), err)
			}
			return stateProtectionError(file.Name(), false, err)
		}
		return nil
	}
	owned, err := OwnedByCurrentUser(file)
	if err == nil && !owned {
		err = errors.New("belongs to another account")
	}
	if err != nil {
		return stateProtectionError(file.Name(), true, err)
	}
	return nil
}

// ProtectManagedStateFiles protects existing managed entries before Git or
// other helpers use them. It does not descend into repositories or workspaces.
func ProtectManagedStateFiles(held *os.File) error {
	return walkManagedState(held, "", inspectForStart, func(file *os.File, directory bool) error {
		return protectStateObject(held.Name(), file, directory)
	})
}

func walkManagedState(parent *os.File, relative string, purpose inspectionPurpose, visit func(*os.File, bool) error) error {
	entries, err := readStateDirectory(parent)
	if err != nil {
		return stateProtectionError(parent.Name(), true, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !statepath.Managed(relative, name) || relative == "" && statepath.DatabaseFile(name) {
			continue
		}
		if err := visitManagedEntry(parent, name, purpose, func(file *os.File, directory bool) error {
			if err := visit(file, directory); err != nil {
				return err
			}
			child := name
			if relative != "" {
				child = relative + "/" + name
			}
			if directory && statepath.HasChildren(child) {
				return walkManagedState(file, child, purpose, visit)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func visitManagedEntry(parent *os.File, name string, purpose inspectionPurpose, visit func(*os.File, bool) error) error {
	path := filepath.Join(parent.Name(), name)
	info, err := lookupSourceEntry(parent, path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return stateProtectionError(path, false, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
		return stateEntryRepairError(path, errors.New("not a plain managed entry"))
	}
	directory := info.IsDir()
	var file *os.File
	if directory || purpose == inspectForReader {
		file, err = openSourceEntry(parent, path, true)
	} else {
		file, err = OpenOwnFile(parent, name, os.O_RDONLY)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		if errors.Is(err, errMultipleFileNames) {
			return stateEntryRepairError(path, err)
		}
		return stateProtectionError(path, directory, err)
	}
	defer file.Close()
	return visit(file, directory)
}

func readStateDirectory(held *os.File) ([]os.DirEntry, error) {
	readable, err := openReadableStateDirectory(held)
	if err != nil {
		return nil, err
	}
	defer readable.Close()
	return readable.ReadDir(-1)
}

func stateEntryRepairError(path string, cause error) error {
	instruction := fmt.Sprintf("replace %q with a regular file or directory without links or extra names, preserving its contents, then start OwnGit again", path)
	return fmt.Errorf("could not protect %s: %w; to fix it, %s", path, cause, instruction)
}
