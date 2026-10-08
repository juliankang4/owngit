package gitexec

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// A reader that gives up must neither hold the lock nor take it later: it
// leaves the queue when its context ends, so the holder's own release is the
// only generation change.
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
	if lock.Waiting() {
		t.Fatal("the reader that gave up is still counted as waiting")
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	acquired := make(chan error, 1)
	go func() { acquired <- lock.LockContext(ctx) }()
	waitForWaiters(t, lock, 1)
	select {
	case err := <-acquired:
		t.Fatalf("writer returned before the reader released: %v", err)
	default:
	}
	lock.RUnlock()
	if err := <-acquired; err != nil {
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
	// A writer that gives up leaves the lock free at once, so the reader that
	// holds it stays the only holder and no late take changes the generation.
	lock.RLock()
	short, cancelShort := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelShort()
	if err := lock.LockContext(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LockContext error=%v, want the deadline", err)
	}
	if lock.Waiting() {
		t.Fatal("the writer that gave up is still counted as waiting")
	}
	generation = lock.Generation()
	lock.RUnlock()
	if !lock.TryLock() {
		t.Fatal("the abandoned writer left the lock taken")
	}
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
	if lock.Waiting() {
		t.Fatalf("waiting callers remain: %d", lock.waiters.Load())
	}
	waitForWriteLock(t, lock)
	lock.Unlock()
}

// Cancelled waiters leave no goroutine behind and no place in the queue: each
// one waits in its own goroutine and leaves the queue when its context ends.
// A lock that parked a helper goroutine per waiter until the holder released
// would keep this test's count near the number of cancelled callers.
func TestCancelledWaitersLeaveNoGoroutines(t *testing.T) {
	lock := NewLocks().For("project")
	lock.Lock()
	before := runtime.NumGoroutine()
	const waiters = 200
	cancels := make([]context.CancelFunc, 0, waiters)
	var group sync.WaitGroup
	for index := range waiters {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		group.Add(1)
		go func(write bool) {
			defer group.Done()
			var err error
			if write {
				err = lock.LockContext(ctx)
			} else {
				err = lock.RLockContext(ctx)
			}
			if err == nil {
				t.Error("a cancelled caller took the lock")
			}
		}(index%2 == 0)
	}
	waitForWaiters(t, lock, waiters)
	for _, cancel := range cancels {
		cancel()
	}
	group.Wait()
	if lock.Waiting() {
		t.Fatalf("waiting callers remain: %d", lock.waiters.Load())
	}
	if after := runtime.NumGoroutine(); after > before+10 {
		t.Fatalf("goroutines before=%d after=%d: %d cancelled waiters left work behind", before, after, waiters)
	}
	lock.Unlock()
	waitForWriteLock(t, lock)
	lock.Unlock()
}

// Waiters are served in arrival order: the reader that arrived first runs
// before the writers behind it, and the writers run in turn.
func TestQueuedWaitersAreServedInArrivalOrder(t *testing.T) {
	lock := NewLocks().For("project")
	lock.Lock()
	served := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if lock.RLockContext(ctx) != nil {
			t.Error("the reader did not take the lock")
			return
		}
		defer lock.RUnlock()
		served <- "reader"
	}()
	waitForWaiters(t, lock, 1)
	for index, name := range [...]string{"first writer", "second writer"} {
		name := name
		go func() {
			lock.Lock()
			defer lock.Unlock()
			served <- name
		}()
		waitForWaiters(t, lock, int32(index+2))
	}
	lock.Unlock()
	for index, want := range [...]string{"reader", "first writer", "second writer"} {
		select {
		case got := <-served:
			if got != want {
				t.Fatalf("waiter %d was served as %q, want %q", index, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("waiter %d (%s) was not served", index, want)
		}
	}
}

// A reader that arrives while a writer waits stays behind it, so a writer
// cannot starve behind a stream of readers.
func TestAWaitingWriterKeepsNewReadersOut(t *testing.T) {
	lock := NewLocks().For("project")
	lock.RLock()
	served := make(chan string, 2)
	go func() {
		lock.Lock()
		defer lock.Unlock()
		served <- "writer"
	}()
	waitForWaiters(t, lock, 1)
	go func() {
		lock.RLock()
		defer lock.RUnlock()
		served <- "reader behind the writer"
	}()
	waitForWaiters(t, lock, 2)
	if lock.TryRLock() {
		lock.RUnlock()
		t.Fatal("a reader took the lock while a writer waited")
	}
	lock.RUnlock()
	for index, want := range [...]string{"writer", "reader behind the writer"} {
		select {
		case got := <-served:
			if got != want {
				t.Fatalf("waiter %d was served as %q, want %q", index, got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("waiter %d (%s) was not served", index, want)
		}
	}
}

// A waiter that gives up at the moment the holder releases must not leave the
// lock taken, and a waiter that wins that moment holds it.
func TestLockHandoverAtCancelKeepsTheLockUsable(t *testing.T) {
	lock := NewLocks().For("project")
	for range 200 {
		lock.Lock()
		ctx, cancel := context.WithCancel(context.Background())
		acquired := make(chan bool, 1)
		go func() {
			acquired <- lock.LockContext(ctx) == nil
		}()
		waitForWaiters(t, lock, 1)
		cancel()
		lock.UnlockWithoutRefChanges()
		if <-acquired {
			lock.UnlockWithoutRefChanges()
		} else if !lock.TryLock() {
			t.Fatal("the lock stayed taken after the waiter gave up")
		} else {
			lock.UnlockWithoutRefChanges()
		}
	}
}

// waitForWaiters fails the test unless want callers are blocked on the lock.
func waitForWaiters(t *testing.T, lock *RepositoryLock, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for lock.waiters.Load() != want {
		if time.Now().After(deadline) {
			t.Fatalf("waiters=%d, want %d", lock.waiters.Load(), want)
		}
		time.Sleep(time.Millisecond)
	}
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
