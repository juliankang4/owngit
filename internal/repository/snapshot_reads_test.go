package repository

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
	"unsafe"

	"owngit/internal/gitexec"
)

func snapshotDerived(t *testing.T, manager *Manager, snapshot RefSnapshot) {
	t.Helper()
	for _, tags := range []bool{false, true} {
		tips, err := manager.RefTipsAt(context.Background(), "sample", snapshot, tags)
		noErr(t, err)
		refs := snapshot.Summary.Branches
		if tags {
			refs = snapshot.Summary.Tags
		}
		for _, ref := range refs {
			if ref.Type == "commit" && tips[ref.Name].OID != ref.OID {
				t.Fatalf("tip %s=%s, snapshot=%s", ref.Name, tips[ref.Name].OID, ref.OID)
			}
		}
	}
	_, err := manager.RetainedRefsAt(context.Background(), "sample", snapshot)
	noErr(t, err)
}

func TestSnapshotDerivedReadsReuseImmutableData(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the command-counting wrapper is a POSIX-shell fixture")
	}
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "old", "old", "2024-01-01T00:00:00Z")
	runGit(t, work, "tag", "-a", "release", "-m", "release")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "refs/tags/release")
	oldTag := gitOutput(t, work, "rev-parse", "release")
	// Retain a deleted annotated tag before taking the snapshot.
	runGit(t, work, "push", "origin", ":refs/tags/release")
	count, _, _ := countGitProcesses(t, manager, "*")
	snapshot := mustSnapshot(t, manager)
	snapshotDerived(t, manager, snapshot)
	retained, err := manager.RetainedRefsAt(context.Background(), "sample", snapshot)
	noErr(t, err)
	if len(retained) != 1 || retained[0].OID != oldTag || retained[0].Commit.Subject != "old" {
		t.Fatalf("retained=%+v", retained)
	}
	before := count()
	snapshotDerived(t, manager, mustSnapshot(t, manager))
	if count() != before {
		t.Fatalf("warm derived reads started %d Git processes", count()-before)
	}
	// Returned maps and slices cannot change another request's data.
	tips, err := manager.RefTipsAt(context.Background(), "sample", snapshot, false)
	noErr(t, err)
	tips["main"] = Commit{Subject: "changed"}
	retained[0].Commit.Subject = "changed"
	again, err := manager.RetainedRefsAt(context.Background(), "sample", snapshot)
	noErr(t, err)
	if again[0].Commit.Subject != "old" {
		t.Fatal("a caller changed cached retained metadata")
	}

	// An outside write must not mix fresh retained refs with the old listing,
	// even when this snapshot's derived reads have not yet been filled.
	manager.snapshots.drop("sample")
	unfilled := mustSnapshot(t, manager)
	commitFile(t, work, "new", "new", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	snapshotDerived(t, manager, unfilled)
	got, err := manager.RetainedRefsAt(context.Background(), "sample", unfilled)
	noErr(t, err)
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("outside write changed old retained data: %+v", got)
	}
	wroteRefs(manager, "sample")
	fresh := mustSnapshot(t, manager)
	snapshotDerived(t, manager, fresh)
	if fresh.Summary.DefaultOID == unfilled.Summary.DefaultOID {
		t.Fatal("an OwnGit write did not refresh the listing")
	}
	// A new serving manager starts with no snapshot or derived cache.
	restarted := &Manager{Store: manager.Store, Git: manager.Git, Locks: gitexec.NewLocks(), Root: manager.RepositoryRoot()}
	snapshotDerived(t, restarted, mustSnapshot(t, restarted))
	if snapshot := mustSnapshot(t, restarted); snapshot.Summary.DefaultOID != fresh.Summary.DefaultOID {
		t.Fatal("a restart did not refresh the listing")
	}
}

func TestSnapshotDerivedRetainedTagFollowsWrites(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "tag", "-a", "release", "-m", "release")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "refs/tags/release")
	before := mustSnapshot(t, manager)
	snapshotDerived(t, manager, before)
	runGit(t, work, "push", "origin", ":refs/tags/release")
	wroteRefs(manager, "sample")
	after := mustSnapshot(t, manager)
	snapshotDerived(t, manager, after)
	retained, err := manager.RetainedRefsAt(context.Background(), "sample", after)
	noErr(t, err)
	if len(before.Summary.Tags) != 1 || len(after.Summary.Tags) != 0 || len(retained) != 1 || retained[0].Kind != "tag" || retained[0].Commit.OID != before.Summary.DefaultOID {
		t.Fatal("tag deletion did not refresh the listing, retained ref and peeled commit together")
	}
	if after.ActivityKey != before.ActivityKey {
		t.Fatal("a tag-only write unexpectedly changed the branch activity key")
	}
}

func TestSnapshotDerivedReadsCoalesceAndStayPinnedAcrossWrites(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the command-counting wrapper is a POSIX-shell fixture")
	}
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	snapshot := mustSnapshot(t, manager)
	count, _, _ := countGitProcesses(t, manager, "*")
	var group sync.WaitGroup
	for index := 0; index < 12; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			tips, err := manager.RefTipsAt(context.Background(), "sample", snapshot, false)
			if err != nil || tips["main"].OID != snapshot.Summary.DefaultOID {
				t.Errorf("concurrent tips=%+v err=%v", tips, err)
			}
		}()
	}
	group.Wait()
	if count() != 1 {
		t.Fatalf("concurrent tips started %d processes, want one", count())
	}
	commitFile(t, work, "two", "two", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/transfer")
	lock := manager.Locks.For("sample")
	lock.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.RefTipsAt(ctx, "sample", snapshot, false); err == nil {
		t.Fatal("a busy derived read did not report its deadline")
	}
	runGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/main", gitOutput(t, work, "rev-parse", "HEAD"))
	lock.Unlock()
	snapshotDerived(t, manager, snapshot)
	snapshotDerived(t, manager, mustSnapshot(t, manager))
}

func TestSnapshotDerivedReadsDoNotHideFailuresOrStaleness(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the failing wrapper is a POSIX-shell fixture")
	}
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "tag", "-a", "release", "-m", "release")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "refs/tags/release")
	count, fail, _ := countGitProcesses(t, manager, "log|cat-file")
	snapshot := mustSnapshot(t, manager)
	noErr(t, os.WriteFile(fail, nil, 0o600))
	for _, tags := range []bool{false, true} {
		if _, err := manager.RefTipsAt(context.Background(), "sample", snapshot, tags); err == nil {
			t.Fatal("failed metadata was reported as known")
		}
	}
	noErr(t, os.Rename(fail, fail+".finished"))
	before := count()
	snapshotDerived(t, manager, snapshot)
	if count() == before {
		t.Fatal("failed derived data was cached")
	}
	snapshot.Stale = true
	if _, err := manager.RefTipsAt(context.Background(), "sample", snapshot, false); err == nil {
		t.Fatal("a stale snapshot served cached success as current")
	}
	if _, err := manager.RetainedRefsAt(context.Background(), "sample", snapshot); err == nil {
		t.Fatal("stale retained data was served as current")
	}
	wroteRefs(manager, "sample")
	noErr(t, os.WriteFile(fail, nil, 0o600))
	// Ref listing succeeds, but a failed head follow-up must not be cached.
	commitFile(t, work, "two", "needs  conversion", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	incomplete := mustSnapshot(t, manager)
	if incomplete.HeadErr == nil {
		t.Fatal("expected a failed head follow-up")
	}
	if entry := manager.snapshots.entries["sample"]; entry.generation == manager.Locks.For("sample").Generation() || entry.snapshot.reads == incomplete.reads {
		t.Fatal("an incomplete snapshot retained a current derived cache")
	}
}

func TestSnapshotDerivedCacheEvictsAndForgets(t *testing.T) {
	var cache snapshotCache
	locks := gitexec.NewLocks()
	for index := 0; index < snapshotCapacity+1; index++ {
		id := fmt.Sprint(index)
		cache.store(id, "root/"+id+".git", locks.For(id), 0, RefSnapshot{reads: &snapshotReads{}})
	}
	if len(cache.entries) != snapshotCapacity {
		t.Fatalf("entries=%d, capacity=%d", len(cache.entries), snapshotCapacity)
	}
	if _, ok := cache.entries["0"]; ok {
		t.Fatal("the least recently used snapshot was not evicted")
	}
	cache.forget([]string{"1"})
	if len(cache.entries) != 1 || cache.entries["1"].snapshot.reads == nil {
		t.Fatal("forget did not drop snapshots and their derived reads together")
	}
}

func TestSnapshotRetainedDataSize(t *testing.T) {
	const retained = 20_000
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	refs := make([]Ref, 0, 2*retained)
	for index := 0; index < retained; index++ {
		oid := fmt.Sprintf("%040x", index+1)
		refs = append(refs, Ref{Name: "refs/owngit/retained/heads/" + oid, OID: oid, Type: "commit"},
			Ref{Name: "refs/owngit/provenance/heads/main/" + oid, OID: oid, Type: "commit"})
	}
	runtime.ReadMemStats(&after)
	t.Logf("retained=%d extra_ref_records=%d record_bytes=%d slice_bytes=%d synthetic_allocation_bytes=%d cache_entries=%d", retained, len(refs), unsafe.Sizeof(Ref{}), len(refs)*int(unsafe.Sizeof(Ref{})), after.TotalAlloc-before.TotalAlloc, snapshotCapacity)
	activityRefs := make([]activityKeyRef, len(refs))
	for index, ref := range refs {
		activityRefs[index] = activityKeyRef{name: ref.Name, oid: ref.OID}
	}
	lock := gitexec.NewLocks().For("large")
	var cache snapshotCache
	reads := &snapshotReads{path: "root/large.git", lock: lock, gate: make(chan struct{}, 1), refs: refs}
	snapshot := RefSnapshot{activityRefs: activityRefs, refs: refs, reads: reads}
	weight := snapshotWeight(snapshot)
	cache.store("large", "root/large.git", lock, 0, snapshot)
	t.Logf("estimated_snapshot_weight=%d total_budget=%d entry_cap=%d cached_entries=%d (shared internal slices, no derived commit metadata)", weight, snapshotByteBudget, snapshotByteBudget/4, len(cache.entries))
	if weight <= snapshotByteBudget/4 || len(cache.entries) != 0 || cache.bytes != 0 {
		t.Fatal("large retained-ref snapshot exceeded the entry cap but was retained")
	}
	runtime.KeepAlive(&cache)
}
