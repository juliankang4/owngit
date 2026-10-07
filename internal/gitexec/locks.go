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
	// gate, when set, runs after every LockContext and TryLockGated takes the
	// write lock. An error from it means the writer must not go on: the lock
	// is released, with no ref change, and the error is returned.
	gate atomic.Pointer[func(string) error]
}

// SetWriteGate installs the check that every write lock taken through
// LockContext or TryLockGated passes. Passing nil removes it.
func (l *Locks) SetWriteGate(gate func(string) error) {
	if gate == nil {
		l.gate.Store(nil)
		return
	}
	l.gate.Store(&gate)
}

// RepositoryLock is one repository's reader and writer lock. Every release of
// the write lock advances its generation, so a reader that saw the same
// generation earlier knows that no OwnGit writer has changed the repository
// since. Unlock advances it even when nothing changed; only a holder that
// provably changed no ref releases through UnlockWithoutRefChanges. Release
// the write lock only through those two methods.
//
// The lock hands out its turns in arrival order and keeps no goroutine per
// waiter: a caller that waits with a context waits in its own goroutine and
// leaves the queue when the context ends, so a request that gives up collects
// nothing and cannot hold another caller back. A waiting writer keeps new
// readers out, so a writer cannot starve behind a stream of readers.
type RepositoryLock struct {
	mu sync.Mutex
	// readers counts the readers that hold the lock.
	readers int
	// writing reports that a writer holds the lock.
	writing bool
	// writersWaiting counts the queued writers. A waiting writer keeps new
	// readers out, as Go's RWMutex does.
	writersWaiting int
	// queue holds the waiters in arrival order. A granted waiter has left the
	// queue and its ready channel is closed.
	queue []*lockWaiter

	generation  atomic.Uint64
	incarnation atomic.Uint64
	// waiters counts callers blocked in Lock, RLock or a context wait.
	waiters atomic.Int32

	// owner supplies the write gate; nil for a lock made outside Locks.
	owner *Locks
	id    string
}

// lockWaiter is one queued caller. ready is closed when it holds the lock.
type lockWaiter struct {
	write bool
	ready chan struct{}
}

// Lock takes the write lock without the write gate, counting the caller as waiting while the lock is
// held by someone else.
func (l *RepositoryLock) Lock() {
	l.mu.Lock()
	if l.writeLockFreeLocked() {
		l.writing = true
		l.mu.Unlock()
		return
	}
	waiter := l.enqueueLocked(true)
	l.mu.Unlock()
	<-waiter.ready
	l.waiters.Add(-1)
}

// RLock takes the read lock, counting the caller as waiting while a writer
// holds or waits for the lock.
func (l *RepositoryLock) RLock() {
	l.mu.Lock()
	if l.readLockFreeLocked() {
		l.readers++
		l.mu.Unlock()
		return
	}
	waiter := l.enqueueLocked(false)
	l.mu.Unlock()
	<-waiter.ready
	l.waiters.Add(-1)
}

// LockContext takes the write lock unless ctx ends first. It then returns
// ctx's error and does not hold the lock. Release a lock it took through
// Unlock or UnlockWithoutRefChanges.
func (l *RepositoryLock) LockContext(ctx context.Context) error {
	if err := l.LockContextUngated(ctx); err != nil {
		return err
	}
	return l.passGate()
}

// LockContextUngated takes the write lock without the gate. Storage preparation
// and recorded lifecycle cleanup verify their storage separately before writing.
// Ordinary writers use LockContext or TryLockGated.
func (l *RepositoryLock) LockContextUngated(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	if l.writeLockFreeLocked() {
		l.writing = true
		l.mu.Unlock()
		return nil
	}
	waiter := l.enqueueLocked(true)
	l.mu.Unlock()
	return l.waitForTurn(ctx, waiter)
}

// passGate runs the write gate of the lock's Locks for a caller that holds
// the write lock, and releases the lock when the gate refuses.
func (l *RepositoryLock) passGate() error {
	if l.owner == nil {
		return nil
	}
	gate := l.owner.gate.Load()
	if gate == nil {
		return nil
	}
	if err := (*gate)(l.id); err != nil {
		l.releaseWriteLock()
		return err
	}
	return nil
}

// TryLockGated is TryLock followed by the write gate: it reports false when
// the lock is not free, and an error, without the lock, when the gate refuses.
func (l *RepositoryLock) TryLockGated() (bool, error) {
	if !l.TryLock() {
		return false, nil
	}
	if err := l.passGate(); err != nil {
		return false, err
	}
	return true, nil
}

// RLockContext takes the read lock unless ctx ends first. It then returns
// ctx's error and does not hold the lock. Request handlers use it, so a
// repository held by a long clone and a queued push cannot keep a page
// waiting past its deadline.
func (l *RepositoryLock) RLockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	if l.readLockFreeLocked() {
		l.readers++
		l.mu.Unlock()
		return nil
	}
	waiter := l.enqueueLocked(false)
	l.mu.Unlock()
	return l.waitForTurn(ctx, waiter)
}

// TryLock takes the write lock and reports whether it is free for this caller.
// It fails while a reader or another writer holds the lock, or while any
// waiter is queued.
func (l *RepositoryLock) TryLock() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.writeLockFreeLocked() {
		return false
	}
	l.writing = true
	return true
}

// TryRLock takes the read lock and reports whether it is free for this caller.
// It fails while a writer holds or waits for the lock, as Go's RWMutex does.
func (l *RepositoryLock) TryRLock() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.readLockFreeLocked() {
		return false
	}
	l.readers++
	return true
}

// Waiting reports whether a caller is blocked waiting for this lock. A holder
// that works in steps, such as repository maintenance, checks this between its
// steps and stops to let the waiting caller in.
func (l *RepositoryLock) Waiting() bool {
	return l.waiters.Load() > 0
}

// Unlock advances the generation and then releases the write lock. The
// generation changes while the writer still holds the lock, so any reader that
// acquires the lock afterwards observes the new generation.
func (l *RepositoryLock) Unlock() {
	l.generation.Add(1)
	l.releaseWriteLock()
}

// UnlockWithoutRefChanges releases the write lock without advancing the
// generation, so cached ref snapshots stay valid. Use it only when the holder
// ran no Git command or file operation that can change refs or HEAD since it
// took the lock. Storing the same objects differently, as repack and
// commit-graph do, or adding objects that no ref reaches, as a refused push
// does, leaves every snapshot as it was. When in doubt, use Unlock.
func (l *RepositoryLock) UnlockWithoutRefChanges() {
	l.releaseWriteLock()
}

// RUnlock releases the read lock.
func (l *RepositoryLock) RUnlock() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.readers == 0 {
		panic("gitexec: RUnlock of unlocked RepositoryLock")
	}
	l.readers--
	l.dispatchLocked()
}

// releaseWriteLock releases the write lock and lets the waiters that may run
// now take it.
func (l *RepositoryLock) releaseWriteLock() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.writing {
		panic("gitexec: Unlock of unlocked RepositoryLock")
	}
	l.writing = false
	l.dispatchLocked()
}

// writeLockFreeLocked reports whether a caller may take the write lock now. A
// queued waiter implies a held lock, so it also blocks, which keeps this
// caller behind the waiters that arrived first.
func (l *RepositoryLock) writeLockFreeLocked() bool {
	return !l.writing && l.readers == 0 && len(l.queue) == 0
}

// readLockFreeLocked reports whether a caller may take the read lock now: a
// writer holds the lock, or a writer waits and would starve otherwise.
func (l *RepositoryLock) readLockFreeLocked() bool {
	return !l.writing && l.writersWaiting == 0
}

// enqueueLocked puts a waiter at the end of the queue, where Waiting sees it
// before it blocks. The caller holds mu and must not hold the lock in a way
// that the waiter wants.
func (l *RepositoryLock) enqueueLocked(write bool) *lockWaiter {
	waiter := &lockWaiter{write: write, ready: make(chan struct{})}
	l.queue = append(l.queue, waiter)
	if write {
		l.writersWaiting++
	}
	l.waiters.Add(1)
	return waiter
}

// waitForTurn waits for the waiter's turn unless ctx ends first, in which case
// it leaves the queue. It returns nil when the caller holds the lock, which
// can happen when the lock arrives at the moment the context ends.
func (l *RepositoryLock) waitForTurn(ctx context.Context, waiter *lockWaiter) error {
	select {
	case <-waiter.ready:
		l.waiters.Add(-1)
		return nil
	case <-ctx.Done():
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-waiter.ready:
		// The lock arrived at the same moment; the caller owns it.
		l.waiters.Add(-1)
		return nil
	default:
	}
	l.dequeueLocked(waiter)
	l.waiters.Add(-1)
	return ctx.Err()
}

// dequeueLocked takes a waiter that gave up out of the queue, so the callers
// behind it move up. The caller holds mu.
func (l *RepositoryLock) dequeueLocked(waiter *lockWaiter) {
	index := -1
	for at, queued := range l.queue {
		if queued == waiter {
			index = at
			break
		}
	}
	if index < 0 {
		return
	}
	copy(l.queue[index:], l.queue[index+1:])
	l.queue[len(l.queue)-1] = nil
	l.queue = l.queue[:len(l.queue)-1]
	if waiter.write {
		l.writersWaiting--
	}
	l.dispatchLocked()
}

// dispatchLocked hands the free lock to the waiters at the front of the queue:
// first every reader that arrived before the first queued writer, then that
// writer once the readers that hold the lock are done. The caller holds mu.
func (l *RepositoryLock) dispatchLocked() {
	for len(l.queue) > 0 && !l.writing {
		next := l.queue[0]
		if !next.write {
			l.popLocked()
			l.readers++
			close(next.ready)
			continue
		}
		if l.readers > 0 {
			return
		}
		l.popLocked()
		l.writersWaiting--
		l.writing = true
		close(next.ready)
		return
	}
}

// popLocked removes the waiter at the front of the queue. The caller holds mu.
func (l *RepositoryLock) popLocked() {
	l.queue[0] = nil
	l.queue = l.queue[1:]
}

// Generation reports how many times the write lock has been released through
// Unlock. It is stable while the caller holds the read lock.
func (l *RepositoryLock) Generation() uint64 {
	return l.generation.Load()
}

// Incarnation identifies a repository lifetime independently of ref writes.
func (l *RepositoryLock) Incarnation() uint64 {
	return l.incarnation.Load()
}

// AdvanceIncarnation invalidates request-local snapshots before a repository
// is removed. The caller holds the write lock; ordinary writes do not call it.
func (l *RepositoryLock) AdvanceIncarnation() {
	l.incarnation.Add(1)
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
		lock = &RepositoryLock{owner: l, id: repositoryID}
		l.locks[repositoryID] = lock
	}
	return lock
}
