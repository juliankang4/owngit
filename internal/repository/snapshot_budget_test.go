package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/gitexec"
)

func TestSnapshotCacheByteBudgetEvictsMixedEntries(t *testing.T) {
	var cache snapshotCache
	locks := gitexec.NewLocks()
	for index := 0; index < 90; index++ {
		count := []int{10, 1800, 20_000}[index%3]
		refs := make([]Ref, count)
		for row := range refs {
			refs[row] = Ref{Name: fmt.Sprintf("refs/heads/topic-%06d", row), OID: fmt.Sprintf("%040x", row+1), Type: "commit"}
		}
		id := fmt.Sprint(index)
		path := "root/" + id + ".git"
		reads := &snapshotReads{path: path, lock: locks.For(id), refs: refs}
		cache.store(id, path, reads.lock, 0, RefSnapshot{refs: refs, reads: reads})
		var total int64
		for _, entry := range cache.entries {
			if entry.weight > snapshotByteBudget/4 {
				t.Fatal("an oversized entry was retained")
			}
			total += entry.weight
		}
		if total != cache.bytes || total > snapshotByteBudget || len(cache.entries) > snapshotCapacity {
			t.Fatalf("weight=%d charged=%d entries=%d", total, cache.bytes, len(cache.entries))
		}
	}
	if len(cache.entries) >= snapshotCapacity {
		t.Fatal("byte budget did not evict below the entry-count cap")
	}
	if _, ok := cache.entries["0"]; ok {
		t.Fatal("oldest entry survived weighted eviction")
	}
	if _, ok := cache.entries["89"]; !ok {
		t.Fatal("newest admissible entry was evicted")
	}
	cache.forget([]string{"89"})
	if cache.bytes != cache.entries["89"].weight {
		t.Fatal("forgetting did not release charged weight")
	}
	cache.drop("89")
	if cache.bytes != 0 {
		t.Fatal("dropping did not release charged weight")
	}
}

func TestSnapshotDerivedGrowthIsChargedAndOversizedResultsAreNotRetained(t *testing.T) {
	var cache snapshotCache
	lock := gitexec.NewLocks().For("sample")
	reads := &snapshotReads{path: "root/sample.git", lock: lock}
	cache.store("sample", reads.path, lock, 0, RefSnapshot{reads: reads})
	initial := cache.bytes
	reads.metadata = map[string]Commit{"oid": {OID: "oid", AuthorName: "Author", Subject: "small"}}
	reads.peeled = map[string]peeledRetainedObject{"tag": {oid: "oid", objectType: "commit"}}
	reads.branches = map[string]Commit{"main": reads.metadata["oid"]}
	reads.retained = []RetainedRef{{Kind: "tag", Source: "refs/tags/release", OID: "tag", Commit: reads.metadata["oid"]}}
	cache.reweigh("sample", reads)
	if cache.bytes <= initial || cache.bytes != snapshotWeight(cache.entries["sample"].snapshot) {
		t.Fatal("derived metadata, peeled tags or rendered result maps were not charged")
	}
	reads.metadata["oversized"] = Commit{Subject: strings.Repeat("x", snapshotByteBudget/4)}
	cache.reweigh("sample", reads)
	if len(cache.entries) != 0 || cache.bytes != 0 {
		t.Fatal("oversized derived data remained in the persistent cache")
	}
	// A late fill of a dropped snapshot must not charge or resurrect it.
	cache.reweigh("sample", reads)
	if cache.bytes != 0 {
		t.Fatal("a dropped snapshot was charged again")
	}
}

func TestSnapshotInternalRefsAreSharedButCallerDataIsIndependent(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "tag", "v1")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "refs/tags/v1")
	first, second := mustSnapshot(t, manager), mustSnapshot(t, manager)
	if &first.refs[0] != &second.refs[0] || &first.activityRefs[0] != &second.activityRefs[0] || &first.refs[0] != &first.reads.refs[0] {
		t.Fatal("immutable internal slices were copied")
	}
	for _, name := range []string{"refs", "activityRefs", "reads"} {
		field := reflect.ValueOf(&first).Elem().FieldByName(name)
		if field.CanSet() || field.CanInterface() {
			t.Fatalf("caller can mutate internal %s", name)
		}
	}
	first.Summary.Branches[0] = Ref{Name: "changed", OID: "changed"}
	first.Summary.Tags[0] = Ref{Name: "changed", OID: "changed"}
	first.Head.Subject = "changed"
	snapshotDerived(t, manager, second)
	tips, err := manager.RefTipsAt(context.Background(), "sample", first, false)
	noErr(t, err)
	if tips["main"].OID != second.Summary.DefaultOID {
		t.Fatal("mutable caller data reached the immutable backing")
	}
	if again := mustSnapshot(t, manager); !reflect.DeepEqual(again, second) {
		t.Fatal("caller changed cached data")
	}
}

func TestSnapshotLargeRefSetAndLongMessagesAreServedWithoutRetention(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	shortOID := gitOutput(t, work, "rev-parse", "HEAD")
	message := filepath.Join(t.TempDir(), "message")
	noErr(t, os.WriteFile(message, []byte("summary only\n\n"+strings.Repeat("long body\n", 512*1024)), 0o600))
	runGit(t, work, "commit", "--allow-empty", "-F", message)
	runGit(t, work, "tag", "-a", "release", "-m", "release")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "refs/tags/release")
	oid := gitOutput(t, work, "rev-parse", "HEAD")
	tag := gitOutput(t, work, "rev-parse", "release")
	var input strings.Builder
	for index := 0; index < 20_000; index++ {
		fmt.Fprintf(&input, "create refs/heads/topic-%06d %s\ncreate refs/tags/tag-%06d %s\n", index, shortOID, index, shortOID)
	}
	fmt.Fprintf(&input, "create refs/owngit/retained/tags/%s %s\ncreate refs/owngit/provenance/tags/gone/%s %s\n", tag, tag, tag, tag)
	_, err := manager.Git.Run(context.Background(), remote, strings.NewReader(input.String()), "--git-dir", ".", "update-ref", "--stdin")
	noErr(t, err)
	for visit := 0; visit < 2; visit++ {
		snapshot := mustSnapshot(t, manager)
		if len(manager.snapshots.entries) != 0 {
			t.Fatal("oversized base snapshot remained cached")
		}
		if len(snapshot.Summary.Branches) != 20_001 || len(snapshot.Summary.Tags) != 20_001 {
			t.Fatal("oversized snapshot lost refs")
		}
		tips, err := manager.RefTipsAt(context.Background(), "sample", snapshot, false)
		noErr(t, err)
		if len(tips) != 20_001 || tips["main"].OID != oid || tips["main"].Subject != "summary only" || tips["main"].Body != "" {
			t.Fatal("oversized branch tips lost render data or retained full messages")
		}
		tags, err := manager.RefTipsAt(context.Background(), "sample", snapshot, true)
		noErr(t, err)
		if len(tags) != 20_001 || tags["release"].OID != oid || tags["release"].Body != "" {
			t.Fatal("oversized tags lost data or retained messages")
		}
		retained, err := manager.RetainedRefsAt(context.Background(), "sample", snapshot)
		noErr(t, err)
		if len(retained) != 1 || retained[0].CommitOID != oid || retained[0].Commit.Body != "" {
			t.Fatal("oversized retained history lost data or retained messages")
		}
		if len(manager.snapshots.entries) != 0 || manager.snapshots.bytes != 0 {
			t.Fatal("an oversized repository remained cached")
		}
	}
}
