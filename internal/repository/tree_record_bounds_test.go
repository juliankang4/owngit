package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
)

type countingTreeReader struct {
	reader io.Reader
	bytes  int
}

func (r *countingTreeReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += n
	return n, err
}

func TestTreeRecordsStopBeforeReadingAnOversizedName(t *testing.T) {
	input := "100644 blob " + strings.Repeat("a", 40) + "       1\t" + strings.Repeat("x", 1<<20) + "\x00"
	reader := &countingTreeReader{reader: strings.NewReader(input)}
	visited := 0
	err := streamTree(reader, func(TreeEntry) error { visited++; return nil }, nil)
	t.Logf("read %d of %d bytes; visited %d entries", reader.bytes, len(input), visited)
	if err == nil || visited != 0 || reader.bytes > 8192 {
		t.Fatalf("oversized record: err=%v visited=%d bytes read=%d", err, visited, reader.bytes)
	}
	for _, after := range []string{"f:" + strings.Repeat("x", 4097), "d:" + strings.Repeat("x", 4097)} {
		if _, err := newTreePageBuilder(after); err == nil {
			t.Error("oversized cursor accepted")
		}
	}
}

func TestOversizedTreeRecordAllocationsStayBounded(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("allocation measurement requires Linux")
	}
	input := "100644 blob " + strings.Repeat("a", 40) + "       1\t" + strings.Repeat("x", 9<<20) + "\x00"
	input += "100644 blob " + strings.Repeat("a", 40) + "       1\tsmall.txt\x00"
	for _, discard := range []bool{false, true} {
		reader := strings.NewReader(input)
		visited, dropped := 0, 0
		var oversized func([]byte) error
		if discard {
			oversized = func(prefix []byte) error { dropped++; return nil }
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := streamTree(reader, func(entry TreeEntry) error {
			visited++
			if entry.Name != "small.txt" {
				t.Fatal("oversized entry visited")
			}
			return nil
		}, oversized)
		runtime.ReadMemStats(&after)
		allocated := after.TotalAlloc - before.TotalAlloc
		if allocated > 1<<20 || (!discard && !errors.As(err, new(*TreeEntryLimitError))) || (discard && (err != nil || visited != 1 || dropped != 1)) {
			t.Fatalf("bounded read discard=%v err=%v allocated=%d visited=%d dropped=%d", discard, err, allocated, visited, dropped)
		}
		t.Logf("9 MiB record discard=%v allocated=%d visited=%d dropped=%d", discard, allocated, visited, dropped)
	}
}

func TestExactTreeLookupDiscardsOversizedRecordsInTheFixedBuffer(t *testing.T) {
	valid := "100644 blob " + strings.Repeat("a", 40) + "       2\tsmall.txt\x00"
	for _, length := range []int{4097, 1 << 20} {
		oversized := "100644 blob " + strings.Repeat("b", 40) + "       2\t" + strings.Repeat("x", length) + "\x00"
		for _, input := range []string{oversized + valid, valid + oversized} {
			visited, dropped := 0, 0
			err := streamTree(strings.NewReader(input), func(entry TreeEntry) error {
				visited++
				if entry.Name != "small.txt" {
					t.Fatal("oversized entry materialized")
				}
				return nil
			}, func(prefix []byte) error {
				dropped++
				if len(prefix) > MaximumTreePathBytes+129 {
					t.Fatal("unbounded prefix")
				}
				return nil
			})
			if err != nil || visited != 1 || dropped != 1 {
				t.Fatalf("length=%d err=%v visited=%d dropped=%d", length, err, visited, dropped)
			}
		}
		if err := streamTree(strings.NewReader(oversized[:len(oversized)-1]), func(TreeEntry) error { return nil }, func([]byte) error { return nil }); err == nil {
			t.Fatal("unterminated discarded record reported successful lookup")
		}
	}
}

func TestFilePagesDistinguishOversizedSiblingsFromTheirContents(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	parent := commitTree(t, manager, work, "main", map[string]string{"keep.txt": "keep\n"})
	blob := hashBareBlob(t, remote, []byte("x\n"))
	healthy := makeBareTree(t, remote, map[string]rawTreeEntry{"inside.txt": {mode: "100644", objectType: "blob", oid: blob}})
	for _, character := range []string{"a", "x"} {
		tree := makeBareTree(t, remote, map[string]rawTreeEntry{
			"small.txt":                     {mode: "100644", objectType: "blob", oid: blob},
			"healthy":                       {mode: "040000", objectType: "tree", oid: healthy},
			strings.Repeat(character, 4097): {mode: "100644", objectType: "blob", oid: blob},
		})
		commit := commitBareTree(t, remote, tree, parent)
		for round := 0; round < 2; round++ {
			view, page, err := manager.PathPageAt(context.Background(), "sample", commit, "small.txt", "")
			if err != nil || view.Folder || view.File.OID != blob || !page.Unavailable || len(view.Entries) != 0 || len(page.Readme) != 0 || page.Total != 0 || page.After != "" {
				t.Fatalf("file lookup round=%d err=%v view=%+v page=%+v", round, err, view, page)
			}
			view, page, err = manager.PathPageAt(context.Background(), "sample", commit, "healthy", "")
			if err != nil || !view.Folder || page.Unavailable || len(view.Entries) != 1 || view.Entries[0].Path != "healthy/inside.txt" {
				t.Fatalf("unrelated parent entry affected healthy folder: err=%v", err)
			}
			_, _, err = manager.PathPageAt(context.Background(), "sample", commit, "", "")
			if !errors.As(err, new(*TreeEntryLimitError)) {
				t.Fatalf("oversized folder err=%v", err)
			}
			_, _, err = manager.PathPageAt(context.Background(), "sample", commit, "missing.txt", "")
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing file err=%v", err)
			}
		}
	}
	// The full path, not just the final component, determines listing failure.
	long := makeBareTree(t, remote, map[string]rawTreeEntry{
		"small.txt":               {mode: "100644", objectType: "blob", oid: blob},
		strings.Repeat("x", 4090): {mode: "100644", objectType: "blob", oid: blob},
	})
	tree := makeBareTree(t, remote, map[string]rawTreeEntry{"nested": {mode: "040000", objectType: "tree", oid: long}})
	commit := commitBareTree(t, remote, tree, parent)
	view, page, err := manager.PathPageAt(context.Background(), "sample", commit, "nested/small.txt", "")
	if err != nil || view.File.OID != blob || !page.Unavailable {
		t.Fatalf("nested file lookup: %v", err)
	}
	_, _, err = manager.PathPageAt(context.Background(), "sample", commit, "nested", "")
	if !errors.As(err, new(*TreeEntryLimitError)) {
		t.Fatalf("nested folder err=%v", err)
	}
}

func TestTreePagesBoundOversizedEntriesAndPreserveMaximumPathBytes(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	parent := commitTree(t, manager, work, "main", map[string]string{"keep.txt": "keep\n"})
	blob := hashBareBlob(t, remote, []byte("raw path\n"))
	for _, length := range []int{4096, 4097, 1 << 20} {
		name := strings.Repeat("x", length-2) + "\xfe\xff"
		tree := makeBareTree(t, remote, map[string]rawTreeEntry{name: {mode: "100644", objectType: "blob", oid: blob}})
		commit := commitBareTree(t, remote, tree, parent)
		for round := 0; round < 2; round++ {
			page, err := manager.TreePageAt(context.Background(), "sample", commit, "", "")
			if length > 4096 {
				if !errors.As(err, new(*TreeEntryLimitError)) || len(page.Entries) != 0 || page.Total != 0 {
					t.Fatalf("oversized page retained entries: length=%d err=%v", length, err)
				}
				continue
			}
			if err != nil || len(page.Entries) != 1 || page.Entries[0].Path != name {
				t.Fatalf("maximum path round %d: %v", round, err)
			}
			result, _, err := cacheTreePage(page)
			if err != nil || len(result.data) > 16<<10 {
				t.Fatalf("maximum path cache bytes=%d err=%v", len(result.data), err)
			}
			if _, err := newTreePageBuilder("f:" + name); err != nil {
				t.Fatalf("maximum path cursor: %v", err)
			}
			view, siblings, err := manager.PathPageAt(context.Background(), "sample", commit, name, "")
			if err != nil || view.File.Path != name || view.File.OID != blob || siblings.Unavailable {
				t.Fatalf("maximum exact file path round %d: %v", round, err)
			}
		}
	}
	// Git and the read lock remain usable after an oversized stream is ended.
	if page, err := manager.TreePageAt(context.Background(), "sample", parent, "", ""); err != nil || len(page.Entries) != 1 {
		t.Fatal(fmt.Sprintf("normal read after rejection: entries=%d err=%v", len(page.Entries), err))
	}
}
