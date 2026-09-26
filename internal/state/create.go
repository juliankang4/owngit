package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"owngit/internal/publishdir"
)

// createDatabase publishes a new, empty database in write-ahead logging mode
// at path. Switching a database to WAL rewrites its header through a rollback
// journal, and the preflight refuses a journal beside the state database, so
// the switch runs on a temporary file in the same directory. The finished file
// then takes the final name without replacing an existing entry. When a
// concurrent first start publishes first, this one discards its file and uses
// that database. A crash before publication leaves only temporary entries,
// which Open ignores, so the final name is either absent or a complete WAL
// database.
func createDatabase(ctx context.Context, path string) (err error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("create state database: %w", err)
	}
	temporary := path + ".new-" + hex.EncodeToString(suffix)
	file, err := CreatePrivateFile(temporary)
	if err != nil {
		return fmt.Errorf("create state database: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, removeTemporaryDatabase(temporary))
		}
	}()
	if err := file.Close(); err != nil {
		return fmt.Errorf("create state database: %w", err)
	}
	db, err := sql.Open("sqlite", sqliteURI(temporary, "mode=rw"))
	if err != nil {
		return fmt.Errorf("create state database: %w", err)
	}
	var mode string
	err = db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode)
	// Closing the only connection checkpoints the empty log and removes the
	// temporary -wal and -shm files. The header change was already synced by
	// the journaled commit.
	if closeErr := db.Close(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err == nil && mode != "wal" {
		err = fmt.Errorf("journal mode is %q", mode)
	}
	if err != nil {
		return fmt.Errorf("create state database: %w", err)
	}
	err = publishNoReplace(ctx, temporary, path)
	if errors.Is(err, fs.ErrExist) {
		return removeTemporaryDatabase(temporary)
	}
	if err != nil {
		return fmt.Errorf("publish state database: %w", err)
	}
	return nil
}

// createLockFile serializes publication on a filesystem that supports neither
// an exclusive rename nor hard links.
const createLockFile = ".database-create.lock"

// publishers are the primitives publishNoReplace tries in order. Tests replace
// them to reach the fallbacks.
var publishers = struct {
	renameExclusive func(oldPath, newPath string) error
	link            func(oldPath, newPath string) error
}{renameExclusive, os.Link}

// publishNoReplace gives the temporary file the final name only when no entry
// exists there, and reports fs.ErrExist otherwise. MoveFileEx without flags
// refuses an existing destination on Windows. Elsewhere an exclusive rename is
// tried first. A filesystem without it falls back to a hard link, and one
// without hard links either, such as exFAT on macOS, to a plain rename under
// a creation lock.
func publishNoReplace(ctx context.Context, temporary, path string) error {
	if runtime.GOOS == "windows" {
		return publishdir.Rename(ctx, temporary, path)
	}
	err := publishers.renameExclusive(temporary, path)
	if !unsupported(err, syscall.EINVAL) {
		return err
	}
	err = publishers.link(temporary, path)
	if err == nil {
		return os.Remove(temporary)
	}
	if !unsupported(err, syscall.EPERM) {
		return err
	}
	return publishUnderLock(temporary, path)
}

// unsupported reports whether err says the filesystem lacks the operation.
// extra is the additional code a filesystem uses for that on Linux: EINVAL
// for an unknown rename flag and EPERM for a refused hard link.
func unsupported(err, extra error) bool {
	return err != nil && (errors.Is(err, errors.ErrUnsupported) || errors.Is(err, extra))
}

// publishUnderLock renames the temporary file into place while it holds the
// creation lock and the final name is still absent. Every opener of this
// version that creates a database on such a filesystem takes the same lock.
// An older version creates the database in place without the lock, and one
// that does so between the check and the rename has its file replaced. That
// needs two different versions making their first start in the same new state
// directory at the same moment, which older versions do not handle among
// themselves either (a concurrent opener can meet their transient rollback
// journal), so the lock does not try to cover it.
func publishUnderLock(temporary, path string) error {
	release, err := AcquireLockBriefly(func() (func(), error) {
		return AcquireExclusiveFileLock(filepath.Join(filepath.Dir(path), createLockFile))
	})
	if err != nil {
		return fmt.Errorf("acquire state database creation lock: %w", err)
	}
	defer release()
	if _, err := os.Lstat(path); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(temporary, path)
}

// removeTemporaryDatabase removes an unpublished temporary database and any
// SQLite side file left beside it.
func removeTemporaryDatabase(temporary string) error {
	var err error
	for _, name := range []string{temporary, temporary + journalSuffix, temporary + walSuffix, temporary + shmSuffix} {
		if removeErr := os.Remove(name); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary state database: %w", removeErr))
		}
	}
	return err
}
