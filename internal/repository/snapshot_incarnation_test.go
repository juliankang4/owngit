package repository

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func newSnapshotLifetimeFixture(t *testing.T) (*Manager, string, RefSnapshot) {
	t.Helper()
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "old", "old", "2024-01-01T00:00:00Z")
	runGit(t, work, "tag", "-a", "release", "-m", "release")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "refs/tags/release")
	snapshot := mustSnapshot(t, manager)
	snapshotDerived(t, manager, snapshot)
	oid, err := manager.RefCommitAt(t.Context(), "sample", snapshot, "refs/tags/release")
	noErr(t, err)
	if oid != snapshot.Summary.DefaultOID {
		t.Fatal("annotated tag was not cached at the snapshot commit")
	}
	return manager, work, snapshot
}

func assertSnapshotRefused(t *testing.T, manager *Manager, snapshot RefSnapshot) {
	t.Helper()
	for _, full := range []string{"refs/heads/main", "refs/tags/release"} {
		if oid, err := manager.RefCommitAt(t.Context(), "sample", snapshot, full); err == nil || oid != "" {
			t.Fatalf("old snapshot accepted: ref=%s oid=%s err=%v", full, oid, err)
		}
	}
	for _, tags := range []bool{false, true} {
		if tips, err := manager.RefTipsAt(t.Context(), "sample", snapshot, tags); err == nil || tips != nil {
			t.Fatalf("old snapshot tips accepted: tags=%v tips=%v err=%v", tags, tips, err)
		}
	}
	if retained, err := manager.RetainedRefsAt(t.Context(), "sample", snapshot); err == nil || retained != nil {
		t.Fatalf("old snapshot retained data accepted: %v %v", retained, err)
	}
}

func TestSnapshotRejectsRecreatedRepository(t *testing.T) {
	manager, work, snapshot := newSnapshotLifetimeFixture(t)
	path, err := manager.Path("sample")
	noErr(t, err)
	lock := manager.Locks.For("sample")
	incarnation := lock.Incarnation()
	_, err = manager.Delete(t.Context(), "sample", DeleteKeepFiles)
	noErr(t, err)
	_, err = manager.Create(t.Context(), "sample", "replacement")
	noErr(t, err)
	newPath, err := manager.Path("sample")
	noErr(t, err)
	if newPath != path || manager.Locks.For("sample") != lock || lock.Incarnation() == incarnation {
		t.Fatal("same-path recreation did not advance the existing lock's incarnation")
	}
	assertSnapshotRefused(t, manager, snapshot)

	commitFile(t, work, "new", "new", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	fresh := mustSnapshot(t, manager)
	oid, err := manager.RefCommitAt(t.Context(), "sample", fresh, "refs/heads/main")
	noErr(t, err)
	if oid == snapshot.Summary.DefaultOID || oid != fresh.Summary.DefaultOID {
		t.Fatal("new snapshot did not select the replacement repository")
	}
	snapshotDerived(t, manager, fresh)
}

func TestSnapshotIncarnationPreservesWritesAndRename(t *testing.T) {
	manager, work, snapshot := newSnapshotLifetimeFixture(t)
	incarnation := manager.Locks.For("sample").Incarnation()
	commitFile(t, work, "new", "new", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	wroteRefs(manager, "sample")
	_, err := manager.Rename(t.Context(), "sample", "Renamed", time.Now())
	noErr(t, err)
	if manager.Locks.For("sample").Incarnation() != incarnation {
		t.Fatal("ordinary write or rename changed repository identity")
	}
	for _, full := range []string{"refs/heads/main", "refs/tags/release"} {
		oid, err := manager.RefCommitAt(t.Context(), "sample", snapshot, full)
		noErr(t, err)
		if oid != snapshot.Summary.DefaultOID {
			t.Fatal("write or rename reinterpreted the in-flight snapshot")
		}
	}
	snapshotDerived(t, manager, snapshot)
	stale := snapshot
	stale.Stale = true
	if _, err := manager.RefCommitAt(t.Context(), "sample", stale, "refs/heads/main"); !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("explicitly stale selection accepted: %v", err)
	}
	root := manager.RepositoryRoot()
	manager.SetRoot(t.TempDir())
	assertSnapshotRefused(t, manager, snapshot)
	manager.SetRoot(root)
}

func TestSnapshotChecksIncarnationAfterWaitingForReadLock(t *testing.T) {
	manager, _, snapshot := newSnapshotLifetimeFixture(t)
	lock := manager.Locks.For("sample")
	lock.Lock()
	held := true
	defer func() {
		if held {
			lock.UnlockWithoutRefChanges()
		}
	}()
	result := make(chan error, 1)
	go func() {
		_, err := manager.RefTipsAt(t.Context(), "sample", snapshot, false)
		result <- err
	}()
	deadline := time.After(3 * time.Second)
	for !lock.Waiting() {
		select {
		case <-deadline:
			t.Fatal("snapshot reader did not wait for the held lock")
		case <-time.After(time.Millisecond):
		}
	}
	lock.AdvanceIncarnation()
	lock.UnlockWithoutRefChanges()
	held = false
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "another repository") {
			t.Fatalf("reader accepted identity changed during its wait: %v", err)
		}
	case <-deadline:
		t.Fatal("snapshot reader did not finish")
	}
}

func TestDeletionReconciliationInvalidatesSnapshots(t *testing.T) {
	manager, _, snapshot := newSnapshotLifetimeFixture(t)
	manager.deletionHook = func(step string) error {
		if step == "recorded" {
			return errors.New("interrupted deletion")
		}
		return nil
	}
	_, err := manager.Delete(t.Context(), "sample", DeleteKeepFiles)
	if !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("deletion did not stop at its recorded intent: %v", err)
	}
	manager.deletionHook = nil
	noErr(t, manager.ReconcileDeletions(t.Context()))
	_, err = manager.Create(t.Context(), "sample", "replacement")
	noErr(t, err)
	assertSnapshotRefused(t, manager, snapshot)
}
