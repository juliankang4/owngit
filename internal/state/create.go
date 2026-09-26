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
	"runtime"

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

// publishNoReplace gives the temporary file the final name only when no entry
// exists there. MoveFileEx without flags refuses an existing destination on
// Windows. POSIX rename replaces one, so elsewhere the file is linked under
// the final name and the temporary name removed.
func publishNoReplace(ctx context.Context, temporary, path string) error {
	if runtime.GOOS == "windows" {
		return publishdir.Rename(ctx, temporary, path)
	}
	if err := os.Link(temporary, path); err != nil {
		return err
	}
	return os.Remove(temporary)
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
