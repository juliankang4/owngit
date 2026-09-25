package repository

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"owngit/internal/gitexec"
)

// snapshotCache keeps the latest ref snapshot of each repository with the
// write generation of the repository lock it was read at. Every OwnGit ref
// write holds that lock, and releasing it advances the generation, so an entry
// is current exactly while the generation is unchanged. Refs written to the
// storage folder without OwnGit are not seen until OwnGit next writes to that
// repository or restarts.
type snapshotCache struct {
	mu sync.Mutex
	// root is the storage folder the entries were read from. Entries of an
	// earlier root are dropped when a snapshot of another root is stored.
	root    string
	entries map[string]snapshotEntry
}

type snapshotEntry struct {
	lock       *gitexec.RepositoryLock
	path       string
	generation uint64
	snapshot   RefSnapshot
}

// RefSnapshot returns the repository's ref snapshot. It reads the refs with
// Git only when an OwnGit writer released the repository's write lock since
// the cached snapshot was read. The existence check runs every time, so a
// missing or unreadable repository still reports an error. Errors and
// snapshots incomplete after a failed follow-up read are never cached.
func (m *Manager) RefSnapshot(ctx context.Context, id string) (RefSnapshot, error) {
	return m.refSnapshot(ctx, id, 0)
}

// RefSnapshotWithin is RefSnapshot for a page that lists many repositories.
// When a Git operation holds the repository, it waits at most wait for the
// lock. It then returns the last snapshot read, marked Stale, even if a write
// changed the refs since, or, without one, an error that wraps
// ErrRepositoryInUse. One busy repository therefore cannot hold up the whole
// page. A read that failed since drops the last snapshot, so a repository
// that could not be read is never shown from an older listing.
func (m *Manager) RefSnapshotWithin(ctx context.Context, id string, wait time.Duration) (RefSnapshot, error) {
	return m.refSnapshot(ctx, id, wait)
}

func (m *Manager) refSnapshot(ctx context.Context, id string, wait time.Duration) (RefSnapshot, error) {
	repositoryPath, _, exists, err := m.ExistingPath(ctx, id)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return RefSnapshot{}, err
	}
	lock := m.Locks.For(id)
	// Without the lock, a writer may be in progress. The cached snapshot then
	// shows the refs before that write, which is the state the write has not
	// yet replaced.
	if snapshot, ok := m.snapshots.lookup(id, repositoryPath, lock); ok {
		return snapshot, nil
	}
	lockContext := ctx
	if wait > 0 {
		var cancel context.CancelFunc
		lockContext, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}
	if err := readLock(lockContext, lock); err != nil {
		if snapshot, ok := m.snapshots.last(id, repositoryPath, lock); ok && wait > 0 && ctx.Err() == nil {
			snapshot.Stale = true
			return snapshot, nil
		}
		return RefSnapshot{}, err
	}
	defer lock.RUnlock()
	generation := lock.Generation()
	snapshot, complete, err := m.readRefSnapshot(ctx, repositoryPath)
	if err != nil {
		if ctx.Err() == nil {
			m.snapshots.drop(id)
		}
		return RefSnapshot{}, err
	}
	// A snapshot missing a field because a follow-up read failed is returned
	// once, like an error, and read again next time.
	if complete {
		m.snapshots.store(id, repositoryPath, lock, generation, snapshot)
	}
	return snapshot, nil
}

// CachedHeadDate returns the author date of the default branch tip from the
// last ref snapshot read for id. It runs no Git process and does not wait for
// the repository lock, so a page can use it for every repository. ok is false
// when no snapshot was read since OwnGit started or the default branch had no
// commit. The date may be older than the refs after an outside write.
func (m *Manager) CachedHeadDate(id string) (time.Time, bool) {
	m.snapshots.mu.Lock()
	defer m.snapshots.mu.Unlock()
	entry, ok := m.snapshots.entries[id]
	if !ok || !entry.snapshot.HeadFound || entry.snapshot.Head.AuthoredAt.IsZero() {
		return time.Time{}, false
	}
	return entry.snapshot.Head.AuthoredAt, true
}

// ForgetRefSnapshots drops cached snapshots and language counts of
// repositories not in present.
func (m *Manager) ForgetRefSnapshots(present []string) {
	m.snapshots.forget(present)
	m.languages.forget(present)
}

func (cache *snapshotCache) lookup(id, path string, lock *gitexec.RepositoryLock) (RefSnapshot, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[id]
	if !ok || entry.lock != lock || entry.path != path || entry.generation != lock.Generation() {
		return RefSnapshot{}, false
	}
	return entry.snapshot.clone(), true
}

// last returns the latest stored snapshot of the repository, whatever writes
// happened since.
func (cache *snapshotCache) last(id, path string, lock *gitexec.RepositoryLock) (RefSnapshot, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[id]
	if !ok || entry.lock != lock || entry.path != path {
		return RefSnapshot{}, false
	}
	return entry.snapshot.clone(), true
}

// store keeps snapshot, read at generation, unless the entry already holds a
// snapshot of the same repository read at the same or a later generation.
func (cache *snapshotCache) store(id, path string, lock *gitexec.RepositoryLock, generation uint64, snapshot RefSnapshot) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if root := filepath.Dir(path); cache.entries == nil || cache.root != root {
		cache.root, cache.entries = root, make(map[string]snapshotEntry)
	}
	if entry, ok := cache.entries[id]; ok && entry.lock == lock && entry.path == path && entry.generation >= generation {
		return
	}
	cache.entries[id] = snapshotEntry{lock: lock, path: path, generation: generation, snapshot: snapshot.clone()}
}

func (cache *snapshotCache) drop(id string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	delete(cache.entries, id)
}

func (cache *snapshotCache) forget(present []string) {
	keep := make(map[string]bool, len(present))
	for _, id := range present {
		keep[id] = true
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for id := range cache.entries {
		if !keep[id] {
			delete(cache.entries, id)
		}
	}
}

// clone copies the slices, so neither a caller nor the cache can change the
// other's snapshot.
func (snapshot RefSnapshot) clone() RefSnapshot {
	snapshot.Summary.Branches = slices.Clone(snapshot.Summary.Branches)
	snapshot.Summary.Tags = slices.Clone(snapshot.Summary.Tags)
	snapshot.Head.Parents = slices.Clone(snapshot.Head.Parents)
	return snapshot
}
