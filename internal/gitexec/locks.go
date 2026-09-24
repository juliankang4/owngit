package gitexec

import (
	"sync"
	"sync/atomic"
)

// Locks coordinates Git writers, readers, and maintenance per repository.
type Locks struct {
	mu    sync.Mutex
	locks map[string]*RepositoryLock
}

// RepositoryLock is one repository's reader and writer lock. Every release of
// the write lock advances its generation, so a reader that saw the same
// generation earlier knows that no OwnGit writer has changed the repository
// since. A release that changed nothing also advances it.
//
// Always release the write lock through this type's Unlock. Unlocking the
// embedded RWMutex directly would skip the generation step.
type RepositoryLock struct {
	sync.RWMutex
	generation atomic.Uint64
}

// Unlock advances the generation and then releases the write lock. The
// generation changes while the writer still holds the lock, so any reader that
// acquires the lock afterwards observes the new generation.
func (l *RepositoryLock) Unlock() {
	l.generation.Add(1)
	l.RWMutex.Unlock()
}

// Generation reports how many times the write lock has been released. It is
// stable while the caller holds the read lock.
func (l *RepositoryLock) Generation() uint64 {
	return l.generation.Load()
}

func NewLocks() *Locks {
	return &Locks{locks: make(map[string]*RepositoryLock)}
}

// For returns the lock of repositoryID. The same ID always returns the same
// lock, so its generation keeps counting across deletion and re-creation.
func (l *Locks) For(repositoryID string) *RepositoryLock {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock := l.locks[repositoryID]
	if lock == nil {
		lock = &RepositoryLock{}
		l.locks[repositoryID] = lock
	}
	return lock
}
