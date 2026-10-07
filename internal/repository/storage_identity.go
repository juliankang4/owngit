package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
)

// repositoryIdentities binds storage to a repository lifetime, not to ref writes.
type repositoryIdentities struct {
	mu       sync.Mutex
	bindings map[string]*repositoryIdentity
}

type repositoryIdentity struct {
	path        string
	info        os.FileInfo
	incarnation uint64
}

// PrepareStorageIdentities prepares an offline manager without writing Git
// configuration or hooks. Existing bindings are verified, never replaced.
func (m *Manager) PrepareStorageIdentities(ctx context.Context) error {
	repositories, err := m.Store.Repositories(ctx)
	if err != nil {
		return err
	}
	for _, stored := range repositories {
		lock := m.Locks.For(stored.ID)
		if err := lock.LockContextUngated(ctx); err != nil {
			return err
		}
		path, err := m.Path(stored.ID)
		if err != nil {
			err = fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
		} else {
			err = m.BindRepositoryStorage(stored.ID, path, nil)
		}
		lock.UnlockWithoutRefChanges()
		if err != nil {
			return fmt.Errorf("prepare repository %q storage: %w", stored.ID, err)
		}
	}
	return nil
}

// BindRepositoryStorage binds a prepared or published directory at its already
// validated final path. The caller holds the write lock. An owned publication's
// identity is remembered even if current access fails, but never before its
// repository row is confirmed. A binding never advances within its lifetime.
func (m *Manager) BindRepositoryStorage(id, path string, expected os.FileInfo) error {
	m.installWriteGate()
	if expected == nil {
		if err := m.VerifyStorageHold(); err != nil {
			return err
		}
	}
	if err := m.rememberRepositoryIdentity(id, path, expected); err != nil {
		return err
	}
	return m.VerifyRepositoryStorage(id)
}

func (m *Manager) rememberRepositoryIdentity(id, path string, expected os.FileInfo) error {
	incarnation := m.Locks.For(id).Incarnation()
	if binding := m.repositoryBinding(id, incarnation); binding != nil {
		if path != binding.path || (expected != nil && !os.SameFile(expected, binding.info)) {
			return fmt.Errorf("%w: %s", ErrStorageChanged, binding.path)
		}
		return nil
	}
	info := expected
	if info == nil {
		var err error
		info, err = captureRepositoryIdentity(path)
		if err != nil {
			if !errors.Is(err, ErrStorageChanged) {
				err = fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
			}
			return fmt.Errorf("inspect repository %q identity: %w", id, err)
		}
	}
	if expected != nil && !directRepositoryDirectory(expected) {
		return fmt.Errorf("repository %q is not a direct directory", id)
	}
	s := &m.storageIdentities
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings == nil {
		s.bindings = make(map[string]*repositoryIdentity)
	}
	s.bindings[id] = &repositoryIdentity{path: path, info: info, incarnation: incarnation}
	return nil
}

// repositoryBinding returns an immutable snapshot. The repository lock keeps
// its lifetime stable; the shared mutex protects only the map lookup.
func (m *Manager) repositoryBinding(id string, incarnation uint64) *repositoryIdentity {
	s := &m.storageIdentities
	s.mu.Lock()
	binding := s.bindings[id]
	s.mu.Unlock()
	if binding != nil && binding.incarnation == incarnation {
		return binding
	}
	return nil
}

// LockCatalogContext serializes catalogue-only changes with the root check.
// The caller releases the returned repository lock through Locks.For(id).
func (m *Manager) LockCatalogContext(ctx context.Context, id string) error {
	lock := m.Locks.For(id)
	if err := lock.LockContextUngated(ctx); err != nil {
		return err
	}
	if err := m.VerifyStorageHold(); err != nil {
		lock.UnlockWithoutRefChanges()
		return err
	}
	return nil
}

// VerifyRepositoryStorage checks the root and directory identities. An unbound
// registered repository refuses writes until preparation binds its directory.
// Unregistered IDs can still take locks for creation or address reservation.
func (m *Manager) VerifyRepositoryStorage(id string) error {
	if err := m.VerifyStorageHold(); err != nil {
		return err
	}
	binding := m.repositoryBinding(id, m.Locks.For(id).Incarnation())
	if binding == nil {
		// The lock gate has no context; bound this catalogue lookup.
		ctx, cancel := context.WithTimeout(context.Background(), creationRecordTimeout)
		defer cancel()
		_, registered, err := m.Store.Repository(ctx, id)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
		}
		if registered {
			return fmt.Errorf("%w: repository %q has not been prepared", ErrStorageUnavailable, id)
		}
		return nil
	}
	return m.verifyRepositoryIdentity(id, binding)
}

// verifyRepositoryIdentity always compares with the original binding. Missing
// storage stays unavailable, and the original directory can return unchanged.
func (m *Manager) verifyRepositoryIdentity(id string, binding *repositoryIdentity) error {
	path, err := m.Path(id)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}
	if path != binding.path {
		return fmt.Errorf("%w: %s", ErrStorageChanged, binding.path)
	}
	info, err := captureRepositoryIdentity(path)
	if err != nil {
		if errors.Is(err, ErrStorageChanged) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
	}
	if !os.SameFile(info, binding.info) {
		return fmt.Errorf("%w: %s", ErrStorageChanged, binding.path)
	}
	return nil
}

func captureRepositoryIdentity(path string) (os.FileInfo, error) {
	info, err := captureDirectoryIdentity(path)
	if err != nil {
		if entry, inspectErr := os.Lstat(path); inspectErr == nil && !directRepositoryDirectory(entry) {
			return nil, fmt.Errorf("%w: %s", ErrStorageChanged, path)
		}
		return nil, err
	}
	entry, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !directRepositoryDirectory(info) || !directRepositoryDirectory(entry) || !os.SameFile(info, entry) {
		return nil, fmt.Errorf("%w: %s", ErrStorageChanged, path)
	}
	return info, nil
}
