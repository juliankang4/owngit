package repository

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// processStart is when this OwnGit process started. Git files older than it
// cannot belong to a Git command that this process runs.
var processStart = time.Now()

// gitTemporaryPackFile matches the files Git writes in objects/pack before it
// renames them into place: pack-objects and index-pack write tmp_pack_XXXXXX
// and tmp_idx_XXXXXX (and the matching rev, mtimes and bitmap files), and
// repack names finished packs .tmp-PID-pack-HASH until it renames them. A
// command that was killed, for example when OwnGit stopped during a repack,
// leaves them behind, and nothing else ever removes them.
var gitTemporaryPackFile = regexp.MustCompile(`^(tmp_(pack|idx|rev|mtimes|bitmap)_[A-Za-z0-9]{6}|\.tmp-[0-9]+-pack-[0-9a-f]+\.(pack|idx|rev|mtimes|bitmap|promisor))$`)

// gitIncomingObjectDirectory matches the quarantine directory that
// receive-pack creates in objects for the objects of a push while it checks
// them (Git's tmp_objdir with the prefix "incoming"). Git removes it when the
// push ends, but a push whose receive-pack was killed leaves it behind with
// the objects received so far, and nothing else ever removes it.
var gitIncomingObjectDirectory = regexp.MustCompile(`^tmp_objdir-incoming-[A-Za-z0-9]{6}$`)

// staleGitLockFiles are the Git lock files that repository maintenance
// creates. A Git process that was killed, which on Windows ends the process
// at once, leaves them behind, and every later pack-refs, branch deletion or
// commit-graph then fails until they are removed. Other lock files are never
// removed.
var staleGitLockFiles = []string{
	"packed-refs.lock",
	filepath.Join("objects", "info", "commit-graphs", "commit-graph-chain.lock"),
}

// removeStaleGitFiles removes the temporary pack files, the push quarantine
// directories and the lock files above that are older than this process. A
// quarantine directory counts as older only when nothing in it changed since
// this process started. The caller holds the repository write lock, so no
// push and no Git command of this process that uses the lock files runs. The
// age check keeps the temporary files of an import, which indexes its pack
// without the lock, because this process started that import. It returns what
// it removed and any errors.
func removeStaleGitFiles(path string) (removed []string, err error) {
	var errs []error
	remove := func(relative string) {
		file := filepath.Join(path, relative)
		info, statErr := os.Lstat(file)
		if errors.Is(statErr, fs.ErrNotExist) {
			return
		}
		if statErr != nil {
			errs = append(errs, statErr)
			return
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(processStart) {
			return
		}
		if removeErr := os.Remove(file); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			errs = append(errs, removeErr)
			return
		}
		removed = append(removed, fmt.Sprintf("%s (%d bytes)", filepath.ToSlash(relative), info.Size()))
	}
	packDirectory := filepath.Join("objects", "pack")
	entries, readErr := os.ReadDir(filepath.Join(path, packDirectory))
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		errs = append(errs, readErr)
	}
	for _, entry := range entries {
		if gitTemporaryPackFile.MatchString(entry.Name()) {
			remove(filepath.Join(packDirectory, entry.Name()))
		}
	}
	for _, name := range staleGitLockFiles {
		remove(name)
	}
	objectEntries, readErr := os.ReadDir(filepath.Join(path, "objects"))
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		errs = append(errs, readErr)
	}
	for _, entry := range objectEntries {
		if !entry.IsDir() || !gitIncomingObjectDirectory.MatchString(entry.Name()) {
			continue
		}
		relative := filepath.Join("objects", entry.Name())
		size, stale, walkErr := staleDirectory(filepath.Join(path, relative))
		if walkErr != nil {
			errs = append(errs, walkErr)
			continue
		}
		if !stale {
			continue
		}
		if removeErr := os.RemoveAll(filepath.Join(path, relative)); removeErr != nil {
			errs = append(errs, removeErr)
			continue
		}
		removed = append(removed, fmt.Sprintf("%s (directory, %d bytes)", filepath.ToSlash(relative), size))
	}
	return removed, errors.Join(errs...)
}

// staleDirectory reports the size of the regular files in directory and
// whether the directory and everything in it were last changed before this
// process started. Links inside are counted by their own times and are not
// followed.
func staleDirectory(directory string) (size int64, stale bool, err error) {
	stale = true
	err = filepath.WalkDir(directory, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(processStart) {
			stale = false
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	return size, stale, err
}

// logStaleGitFileRemoval removes stale Git files from repository id at path
// and logs the outcome. It never fails the caller: a file that stays only
// keeps its current effect.
func logStaleGitFileRemoval(id, path string, logf func(string, ...any)) {
	removed, err := removeStaleGitFiles(path)
	if len(removed) != 0 {
		logf("repository %q: removed files left by a Git command that was interrupted before OwnGit started: %s", id, strings.Join(removed, ", "))
	}
	if err != nil {
		logf("repository %q: could not remove files left by an interrupted Git command: %v", id, err)
	}
}
