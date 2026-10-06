package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
)

// LogStagingName is the private folder, inside the log folder, where macOS
// log files are made before they are published. Its access list is cleared
// before any file is made in it, so no file there inherits an entry.
const LogStagingName = ".owngit-staging"

// LogFolder is a held log folder. On macOS OwnGit keeps an exclusive lock on
// it while it is open, and replaces log files only while holding the lock, so
// a second OwnGit process that writes the same log never has its files
// replaced under it.
type LogFolder struct {
	Dir *os.File
	// Notes say what could not be done for the owner's privacy; the caller
	// puts them in the log or prints them.
	Notes  []string
	locked bool
}

// OpenLogFolder opens the log folder like OpenDirectory with create set. On
// macOS it removes an access list that gives other accounts access, so that
// a file made in the folder afterwards inherits none, but only when the
// folder is OwnGit's own: dedicated says so, or OwnGit creates it now. A
// shared folder, such as Homebrew's log folder, is left alone and a note
// names it.
func OpenLogFolder(path string, dedicated bool) (*LogFolder, error) {
	_, statErr := os.Stat(path)
	dir, err := OpenDirectory(path, true)
	if err != nil {
		return nil, err
	}
	locked, why := lockFolder(dir, path)
	folder := &LogFolder{Dir: dir, locked: locked}
	if why != "" {
		folder.Notes = append(folder.Notes, why)
	}
	permits, err := accessListPermits(dir)
	switch {
	case err != nil:
		folder.Notes = append(folder.Notes, fmt.Sprintf("the access list of %s could not be read: %v", path, err))
	case permits && (dedicated || errors.Is(statErr, fs.ErrNotExist)):
		if err := clearFolderAccessList(dir); err != nil {
			folder.Notes = append(folder.Notes, fmt.Sprintf("the log folder could not be made private: %v", ExplainPrivateFileError(path, err)))
		}
	case permits:
		folder.Notes = append(folder.Notes, sharedFolderNote(path))
	}
	return folder, nil
}

// Replace gives the log file name in the folder a private copy, unless
// OwnGit published that file (see replaceWithPrivateCopy). It does nothing
// except on macOS, and not when another process holds the folder.
func (folder *LogFolder) Replace(name string) error {
	if !folder.locked || runtime.GOOS != "darwin" {
		return nil
	}
	return replaceWithPrivateCopy(folder.Dir, name)
}

// OpenLogFile opens the log file name in the held folder dir for appending,
// and creates it when missing. On macOS a new file is created in a private
// staging folder and published at its name only when it carries no access
// list entry, so no other account can open it while an inherited entry is
// still on it; elsewhere it is created in place, private to this account.
func OpenLogFile(dir *os.File, name string) (*os.File, error) {
	file, err := OpenOwnFile(dir, name, os.O_WRONLY|os.O_APPEND)
	if errors.Is(err, fs.ErrNotExist) {
		return createLogFile(dir, name)
	}
	return file, err
}

// Close releases the folder and its lock.
func (folder *LogFolder) Close() error { return folder.Dir.Close() }
