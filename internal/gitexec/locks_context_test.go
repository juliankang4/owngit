package gitexec

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// A reader that gives up must neither hold the lock nor leave it held later:
// its waiting goroutine takes and releases the lock without advancing the
// generation.
func TestRLockContextGivesUpWithoutKeepingTheLock(t *testing.T) {
	lock := NewLocks().For("project")
	lock.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := lock.RLockContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RLockContext error=%v, want the deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("RLockContext returned after %s", elapsed)
	}
	generation := lock.Generation()
	lock.Unlock()
	waitForWriteLock(t, lock)
	if lock.Generation() != generation+1 {
		t.Fatalf("generation %d, want %d: the abandoned reader must not count as a write", lock.Generation(), generation+1)
	}
	lock.Unlock()
}

// Go's RWMutex blocks new readers behind a waiting writer, so a reader of a
// repository with a long clone and a queued push waits for both. With a
// context, it stops at the deadline.
func TestRLockContextStopsBehindAQueuedWriter(t *testing.T) {
	lock := NewLocks().For("project")
	lock.RLock() // the long clone
	writerDone := make(chan struct{})
	go func() {
		lock.Lock() // the queued push
		lock.Unlock()
		close(writerDone)
	}()
	for lock.TryRLock() {
		lock.RUnlock()
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := lock.RLockContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RLockContext error=%v, want the deadline", err)
	}
	lock.RUnlock()
	<-writerDone
	if err := lock.RLockContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	lock.RUnlock()
	waitForWriteLock(t, lock)
	lock.Unlock()
}

func TestLockContextWaitsForTheHolderAndThenOwnsTheLock(t *testing.T) {
	lock := NewLocks().For("project")
	lock.RLock()
	go func() {
		time.Sleep(30 * time.Millisecond)
		lock.RUnlock()
	}()
	if err := lock.LockContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lock.TryRLock() {
		t.Fatal("LockContext returned without holding the write lock")
	}
	generation := lock.Generation()
	lock.Unlock()
	if lock.Generation() != generation+1 {
		t.Fatal("Unlock after LockContext did not advance the generation")
	}
	// An abandoned writer takes and releases the lock later without
	// counting as a write.
	lock.RLock()
	short, cancelShort := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelShort()
	if err := lock.LockContext(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LockContext error=%v, want the deadline", err)
	}
	generation = lock.Generation()
	lock.RUnlock()
	waitForWriteLock(t, lock)
	if lock.Generation() != generation {
		t.Fatal("the abandoned writer advanced the generation")
	}
	lock.UnlockWithoutRefChanges()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lock.LockContext(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("LockContext with an ended context error=%v", err)
	}
	waitForWriteLock(t, lock)
	lock.Unlock()
}

// Many waiters that give up at random moments leave the lock free.
func TestLockContextRaceLeavesTheLockFree(t *testing.T) {
	lock := NewLocks().For("project")
	var group sync.WaitGroup
	for index := range 200 {
		group.Add(1)
		go func() {
			defer group.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(index%7)*time.Millisecond)
			defer cancel()
			if index%3 == 0 {
				if lock.LockContext(ctx) == nil {
					time.Sleep(100 * time.Microsecond)
					lock.UnlockWithoutRefChanges()
				}
				return
			}
			if lock.RLockContext(ctx) == nil {
				time.Sleep(100 * time.Microsecond)
				lock.RUnlock()
			}
		}()
	}
	group.Wait()
	waitForWriteLock(t, lock)
	lock.Unlock()
}

// waitForWriteLock fails the test unless the write lock becomes free soon,
// which proves that no abandoned waiter kept it.
func waitForWriteLock(t *testing.T, lock *RepositoryLock) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !lock.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("the lock stayed held")
		}
		time.Sleep(time.Millisecond)
	}
}

// Waiting reports callers blocked in Lock, RLock or a context wait, and
// nobody once they hold the lock or gave up.
func TestWaitingCountsBlockedCallers(t *testing.T) {
	lock := NewLocks().For("project")
	lock.Lock()
	if lock.Waiting() {
		t.Fatal("Waiting with no waiter")
	}
	ctx, cancel := context.WithCancel(context.Background())
	var group sync.WaitGroup
	for _, take := range []func(){
		func() { lock.Lock(); lock.Unlock() },
		func() { lock.RLock(); lock.RUnlock() },
		func() {
			if lock.LockContext(context.Background()) == nil {
				lock.Unlock()
			}
		},
		func() {
			// The lock may arrive together with the cancellation.
			if lock.RLockContext(ctx) == nil {
				lock.RUnlock()
			}
		},
	} {
		group.Add(1)
		go func() {
			defer group.Done()
			take()
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for lock.waiters.Load() != 4 {
		if time.Now().After(deadline) {
			t.Fatalf("waiters=%d, want 4", lock.waiters.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if !lock.Waiting() {
		t.Fatal("Waiting is false with blocked callers")
	}
	cancel()
	lock.Unlock()
	group.Wait()
	waitForWriteLock(t, lock)
	if lock.Waiting() {
		t.Fatal("Waiting is true after every caller finished")
	}
	lock.Unlock()
}
