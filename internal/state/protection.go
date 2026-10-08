package state

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func protectStateObject(root string, file *os.File, directory bool) error {
	if !directory {
		if err := requireOwnStateFile(file); err != nil {
			if errors.Is(err, errMultipleFileNames) {
				return stateEntryRepairError(file.Name(), err)
			}
			return stateProtectionError(file.Name(), false, err)
		}
	}
	owned, err := OwnedByCurrentUser(file)
	if err != nil || !owned {
		if err == nil {
			err = errors.New("belongs to another account")
		}
		return stateProtectionError(file.Name(), directory, err)
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

// ProtectManagedStateFiles protects existing managed entries before Git or
// other helpers use them. It does not descend into repositories or workspaces.
func ProtectManagedStateFiles(held *os.File) error {
	files := []string{
		HealthRunFile, TrayAccessFile, TrayHiddenFile, TrayNotificationsFile, TrayCursorFile,
		offlineLockFile, RunningNetworkLockFile, TailscaleChangeLockFile, createLockFile, upgradeBackupOffName,
		"owner-setup.html", ".owner-setup.lock", ".owner-setup.issue.json", "serve-error.txt",
	}
	entries, err := readStateDirectory(held)
	if err != nil {
		return stateProtectionError(held.Name(), true, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if managedStateTemporary(name) {
			files = append(files, name)
		}
	}
	for _, name := range files {
		if err := protectManagedEntry(held, held, name, false); err != nil {
			return err
		}
	}
	for _, name := range []string{"runtime", importCredentialDir, "logs"} {
		path := filepath.Join(held.Name(), name)
		info, err := lookupSourceEntry(held, path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return stateProtectionError(path, true, err)
		}
		if info.Mode().IsRegular() {
			if err := protectManagedEntry(held, held, name, false); err != nil {
				return err
			}
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return stateEntryRepairError(path, errors.New("not a plain managed entry"))
		}
		dir, err := openSourceEntry(held, path, true)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return stateProtectionError(path, true, err)
		}
		err = protectStateObject(held.Name(), dir, true)
		if err == nil {
			switch name {
			case "runtime":
				for _, child := range []string{"git-home", "tmp"} {
					if err = protectManagedEntry(held, dir, child, true); err != nil {
						break
					}
				}
				if err == nil {
					err = protectManagedEntry(held, dir, "gitconfig.empty", false)
				}
			case "logs":
				for _, child := range []string{"service.log", "service.log.1"} {
					if err = protectManagedEntry(held, dir, child, false); err != nil {
						break
					}
				}
			case importCredentialDir:
				var children []os.DirEntry
				children, err = readStateDirectory(dir)
				for _, child := range children {
					if err != nil {
						break
					}
					if managedImportCredential(child.Name()) {
						err = protectManagedEntry(held, dir, child.Name(), false)
					}
				}
			}
		}
		dir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func protectManagedEntry(root, parent *os.File, name string, directory bool) error {
	path := filepath.Join(parent.Name(), name)
	info, err := lookupSourceEntry(parent, path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return stateProtectionError(path, directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
		return stateEntryRepairError(path, errors.New("not a plain managed entry"))
	}
	directory = info.IsDir()
	var file *os.File
	if directory {
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
	return protectStateObject(root.Name(), file, directory)
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

func managedStateTemporary(name string) bool {
	if suffix, ok := strings.CutPrefix(name, databaseName+".new-"); ok {
		decoded, err := hex.DecodeString(suffix)
		return err == nil && len(decoded) == 8
	}
	for _, prefix := range []string{"." + HealthRunFile + "-", "." + TrayAccessFile + "-", "." + TrayNotificationsFile + "-", "." + TrayCursorFile + "-"} {
		if suffix, ok := strings.CutPrefix(name, prefix); ok {
			decoded, err := base64.RawURLEncoding.DecodeString(suffix)
			return err == nil && len(decoded) == 8
		}
	}
	for _, prefix := range []string{".owner-setup-", ".owner-setup-journal-"} {
		if suffix, ok := strings.CutPrefix(name, prefix); ok && suffix != "" && strings.Trim(suffix, "0123456789") == "" {
			return true
		}
	}
	return false
}

func managedImportCredential(name string) bool {
	if strings.HasSuffix(name, ".json") {
		return validText(strings.TrimSuffix(name, ".json"), 100)
	}
	for _, separator := range []string{".tmp-", ".restore-"} {
		id, suffix, ok := strings.Cut(strings.TrimPrefix(name, "."), separator)
		if decoded, err := hex.DecodeString(suffix); ok && validText(id, 100) && err == nil && len(decoded) == 8 {
			return true
		}
	}
	return false
}
