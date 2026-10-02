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
	err := streamTree(reader, func(TreeEntry) error { visited++; return nil })
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
	reader := strings.NewReader(input)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := streamTree(reader, func(TreeEntry) error { t.Fatal("oversized entry visited"); return nil })
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	if !errors.As(err, new(*TreeEntryLimitError)) || allocated > 1<<20 {
		t.Fatalf("bounded read err=%v allocated=%d", err, allocated)
	}
	t.Logf("9 MiB record rejected with %d allocated bytes", allocated)
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
		}
	}
	// Git and the read lock remain usable after an oversized stream is ended.
	if page, err := manager.TreePageAt(context.Background(), "sample", parent, "", ""); err != nil || len(page.Entries) != 1 {
		t.Fatal(fmt.Sprintf("normal read after rejection: entries=%d err=%v", len(page.Entries), err))
	}
}
