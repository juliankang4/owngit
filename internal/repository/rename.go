package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"owngit/internal/state"
)

// Rename gives repository id a new name. The repository then answers at the
// lowercase name, and its earlier address redirects there for
// state.RepositoryAliasLifetime. The ID, the storage folder and every record
// keyed by the ID stay as they are.
//
// A rename is refused, with nothing changed, while the repository is busy:
// an import or a check is running, a Git operation or maintenance holds the
// repository, or a backup is reading it.
func (m *Manager) Rename(ctx context.Context, id, name string, now time.Time) (state.Repository, error) {
	name = strings.TrimSpace(name)
	if err := ValidateName(name, ""); err != nil {
		return state.Repository{}, err
	}
	if err := deletionBusyError(m.Store.RepositoryDeletionBusy(ctx, id)); err != nil {
		return state.Repository{}, err
	}
	lock := m.Locks.For(id)
	if err := lockWithin(ctx, lock, deleteLockWait); err != nil {
		return state.Repository{}, err
	}
	defer lock.Unlock()
	// Creating or importing a repository holds the lock of its ID from its
	// name check until it is recorded, so a rename to that name waits for it.
	if address := strings.ToLower(name); address != id {
		addressLock := m.Locks.For(address)
		if err := lockWithin(ctx, addressLock, deleteLockWait); err != nil {
			return state.Repository{}, err
		}
		defer addressLock.Unlock()
	}
	var renamed state.Repository
	err := m.unlessHeldForBackup(id, func() (err error) {
		renamed, err = m.Store.RenameRepository(ctx, id, name, now)
		return err
	})
	switch {
	case errors.Is(err, ErrBackupReading):
		return state.Repository{}, err
	case errors.Is(err, state.ErrRepositoryNotFound):
		return state.Repository{}, ErrRepositoryNotFound
	case errors.Is(err, state.ErrRepositoryNameTaken):
		return state.Repository{}, fmt.Errorf("%w: %w", ErrNameTaken, err)
	case err != nil:
		return state.Repository{}, deletionBusyError(err)
	}
	return renamed, nil
}
