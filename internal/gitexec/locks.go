package gitexec

import (
	"context"
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
// since. Unlock advances it even when nothing changed; only a holder that
// provably changed no ref releases through UnlockWithoutRefChanges.
//
// Always release the write lock through this type's Unlock or
// UnlockWithoutRefChanges. Unlocking the embedded RWMutex directly would skip
// that decision.
type RepositoryLock struct {
	sync.RWMutex
	generation atomic.Uint64
	// waiters counts callers blocked in Lock, RLock or a context wait.
	waiters atomic.Int32
}

// Lock takes the write lock, counting the caller as waiting while the lock
// is held by someone else.
func (l *RepositoryLock) Lock() {
	if l.RWMutex.TryLock() {
		return
	}
	l.waiters.Add(1)
	defer l.waiters.Add(-1)
	l.RWMutex.Lock()
}

// RLock takes the read lock, counting the caller as waiting while a writer
// holds or waits for the lock.
func (l *RepositoryLock) RLock() {
	if l.RWMutex.TryRLock() {
		return
	}
	l.waiters.Add(1)
	defer l.waiters.Add(-1)
	l.RWMutex.RLock()
}

// Waiting reports whether a caller is blocked waiting for this lock.
// Go's RWMutex lets a holder that releases the write lock and immediately
// calls TryLock again win over a blocked writer, so a holder that works in
// steps, such as repository maintenance, checks this between its steps and
// stops to let the waiting caller in.
func (l *RepositoryLock) Waiting() bool {
	return l.waiters.Load() > 0
}

// Unlock advances the generation and then releases the write lock. The
// generation changes while the writer still holds the lock, so any reader that
// acquires the lock afterwards observes the new generation.
func (l *RepositoryLock) Unlock() {
	l.generation.Add(1)
	l.RWMutex.Unlock()
}

// UnlockWithoutRefChanges releases the write lock without advancing the
// generation, so cached ref snapshots stay valid. Use it only when the holder
// ran no Git command or file operation that can change refs, HEAD, or the
// repository's objects since it took the lock. Storing the same objects
// differently, as repack and commit-graph do, does not change them. When in
// doubt, use Unlock.
func (l *RepositoryLock) UnlockWithoutRefChanges() {
	l.RWMutex.Unlock()
}

// RLockContext takes the read lock unless ctx ends first. It then returns
// ctx's error and does not hold the lock. Request handlers use it, so a
// repository held by a long clone and a queued push cannot keep a page
// waiting past its deadline.
func (l *RepositoryLock) RLockContext(ctx context.Context) error {
	return lockContext(ctx, l.RWMutex.TryRLock, l.RLock, l.RWMutex.RUnlock)
}

// LockContext takes the write lock unless ctx ends first. It then returns
// ctx's error and does not hold the lock. Release a lock it took through
// Unlock or UnlockWithoutRefChanges.
func (l *RepositoryLock) LockContext(ctx context.Context) error {
	return lockContext(ctx, l.RWMutex.TryLock, l.Lock, l.RWMutex.Unlock)
}

// lockContext waits for lock in a goroutine. If ctx ends first, the goroutine
// still takes the lock when it becomes free and releases it at once. That
// release changes nothing, so it does not advance the generation.
func lockContext(ctx context.Context, try func() bool, lock, release func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if try() {
		return nil
	}
	const (
		waiting = iota
		handedOver
		abandoned
	)
	var claim atomic.Int32
	acquired := make(chan struct{})
	go func() {
		lock()
		if claim.CompareAndSwap(waiting, handedOver) {
			close(acquired)
			return
		}
		release()
	}()
	select {
	case <-acquired:
		return nil
	case <-ctx.Done():
		if claim.CompareAndSwap(waiting, abandoned) {
			return ctx.Err()
		}
		// The lock arrived at the same moment; the caller owns it.
		<-acquired
		return nil
	}
}

// Generation reports how many times the write lock has been released through
// Unlock. It is
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
