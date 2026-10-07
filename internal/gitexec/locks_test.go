package gitexec

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestRepositoryWriteGateGetsIDAndReleasesRefusal(t *testing.T) {
	locks := NewLocks()
	refused := errors.New("storage changed")
	locks.SetWriteGate(func(id string) error {
		if id == "sample" {
			return refused
		}
		return nil
	})
	lock := locks.For("sample")
	if err := lock.LockContext(context.Background()); !errors.Is(err, refused) {
		t.Fatalf("write lock error=%v", err)
	}
	if ok, err := lock.TryLockGated(); ok || !errors.Is(err, refused) {
		t.Fatalf("try write lock=%v, error=%v", ok, err)
	}
	if lock.Generation() != 0 || !lock.TryRLock() {
		t.Fatal("refusal changed refs or kept the write lock")
	}
	lock.RUnlock()
	other := locks.For("other")
	if err := other.LockContext(context.Background()); err != nil {
		t.Fatalf("gate received the wrong repository ID: %v", err)
	}
	other.UnlockWithoutRefChanges()
}

func TestRepositoryIncarnationIsIndependentOfRefWrites(t *testing.T) {
	locks := NewLocks()
	lock := locks.For("project")
	lock.Lock()
	lock.Unlock()
	if lock.Generation() != 1 || lock.Incarnation() != 0 {
		t.Fatal("ordinary write changed repository lifetime")
	}
	lock.Lock()
	lock.AdvanceIncarnation()
	if lock.Incarnation() != 1 || lock.Generation() != 1 {
		t.Fatal("incarnation advance also changed write generation")
	}
	lock.UnlockWithoutRefChanges()
	if locks.For("project") != lock || lock.Incarnation() != 1 || lock.Generation() != 1 || locks.For("other").Incarnation() != 0 {
		t.Fatal("repository identity was not retained by its per-ID lock")
	}
}

func TestRepositoryLockGenerationCountsWriteReleases(t *testing.T) {
	locks := NewLocks()
	lock := locks.For("project")
	if locks.For("project") != lock || locks.For("other") == lock {
		t.Fatal("For must return one lock per repository")
	}
	if lock.Generation() != 0 {
		t.Fatalf("new lock generation=%d, want 0", lock.Generation())
	}
	lock.RLock()
	lock.RUnlock()
	if !lock.TryRLock() {
		t.Fatal("TryRLock failed on a free lock")
	}
	lock.RUnlock()
	if lock.Generation() != 0 {
		t.Fatalf("readers advanced the generation to %d", lock.Generation())
	}

	lock.Lock()
	lock.Unlock()
	if lock.Generation() != 1 {
		t.Fatalf("after Lock and Unlock generation=%d, want 1", lock.Generation())
	}
	if !lock.TryLock() {
		t.Fatal("TryLock failed on a free lock")
	}
	lock.Unlock()
	if lock.Generation() != 2 {
		t.Fatalf("after TryLock and Unlock generation=%d, want 2", lock.Generation())
	}
	// A caller that only knows the lock as a sync.Locker still releases it
	// through RepositoryLock.Unlock.
	var locker sync.Locker = locks.For("project")
	locker.Lock()
	locker.Unlock()
	if lock.Generation() != 3 {
		t.Fatalf("after sync.Locker Unlock generation=%d, want 3", lock.Generation())
	}
}

// TestRepositoryLockGenerationChangesBeforeReaders proves that a reader
// waiting for a writer observes the writer's generation once it gets the lock.
func TestRepositoryLockGenerationChangesBeforeReaders(t *testing.T) {
	lock := NewLocks().For("project")
	lock.Lock()
	observed := make(chan uint64)
	go func() {
		lock.RLock()
		defer lock.RUnlock()
		observed <- lock.Generation()
	}()
	lock.Unlock()
	if generation := <-observed; generation != 1 {
		t.Fatalf("reader after the writer saw generation %d, want 1", generation)
	}
}
