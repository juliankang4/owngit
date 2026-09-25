package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Reads for a request stop at its deadline while a Git operation holds the
// repository, and say that the repository is in use. A page that lists
// repositories waits only briefly and then uses the last listing.
func TestRepositoryReadsStopAtTheRequestDeadlineWhileInUse(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "one\n", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	lock := manager.Locks.For("sample")
	lock.Lock()
	held := true
	defer func() {
		if held {
			lock.Unlock()
		}
	}()
	short := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 100*time.Millisecond)
	}

	ctx, cancel := short()
	_, err := manager.RefSnapshot(ctx, "sample")
	cancel()
	if !errors.Is(err, ErrRepositoryInUse) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RefSnapshot error=%v", err)
	}
	ctx, cancel = short()
	_, _, err = manager.Tree(ctx, "sample", "main", "")
	cancel()
	if !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("Tree error=%v", err)
	}
	if !manager.InUse("sample") {
		t.Fatal("InUse is false while a writer holds the repository")
	}
	started := time.Now()
	if _, err := manager.RefSnapshotWithin(context.Background(), "sample", 50*time.Millisecond); !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("RefSnapshotWithin without an earlier listing error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("RefSnapshotWithin waited %s", elapsed)
	}

	// An earlier listing is used even though a write happened since.
	lock.Unlock()
	held = false
	listed, err := manager.RefSnapshot(context.Background(), "sample")
	noErr(t, err)
	commitFile(t, work, "two\n", "two", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	lock.Lock()
	lock.Unlock()
	lock.Lock()
	held = true
	stale, err := manager.RefSnapshotWithin(context.Background(), "sample", 50*time.Millisecond)
	noErr(t, err)
	if stale.Summary.DefaultOID != listed.Summary.DefaultOID {
		t.Fatalf("RefSnapshotWithin returned %s, want the earlier listing %s", stale.Summary.DefaultOID, listed.Summary.DefaultOID)
	}
	lock.Unlock()
	held = false
	if manager.InUse("sample") {
		t.Fatal("InUse is true for a free repository")
	}
	current, err := manager.RefSnapshotWithin(context.Background(), "sample", 50*time.Millisecond)
	noErr(t, err)
	if current.Summary.DefaultOID == listed.Summary.DefaultOID {
		t.Fatal("a free repository returned the earlier listing")
	}
}
