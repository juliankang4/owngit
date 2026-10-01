package repository

import (
	"context"
	"fmt"
	"testing"
)

func TestCachedDirectoryPagesPreservePathBytes(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	parent := commitTree(t, manager, work, "main", map[string]string{"keep.txt": "keep\n"})
	name := "file-\xff.txt"
	blob := hashBareBlob(t, remote, []byte("raw path\n"))
	tree := makeBareTree(t, remote, map[string]rawTreeEntry{name: {mode: "100644", objectType: "blob", oid: blob}})
	commit := commitBareTree(t, remote, tree, parent)
	for round := 0; round < 2; round++ {
		page, err := manager.TreePageAt(context.Background(), "sample", commit, "", "")
		if err != nil || len(page.Entries) != 1 || page.Entries[0].Path != name {
			t.Fatalf("round %d path bytes: %+v %v", round, page.Entries, err)
		}
	}
}

func TestDirectoryPagesKeepEveryEntryAtTheOriginalCommit(t *testing.T) {
	manager, _, work := newTestRepository(t)
	files := map[string]string{"readme.MD": "# Independent README\n", "a-folder/keep.txt": "keep\n", "[literal]/x.txt": "literal\n", "Upper.txt": "upper\n", "lower.txt": "lower\n"}
	for i := 0; i < DirectoryPageEntries*2+7; i++ {
		files[fmt.Sprintf("f%04d.txt", i)] = "x\n"
	}
	oid := commitTree(t, manager, work, "main", files)
	seen := map[string]bool{}
	after := ""
	for round := 0; ; round++ {
		page, err := manager.TreePageAt(context.Background(), "sample", oid, "", after)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) > DirectoryPageEntries || page.Before != len(seen) || page.Total != len(files) {
			t.Fatalf("page %d: entries=%d before=%d total=%d", round, len(page.Entries), page.Before, page.Total)
		}
		if len(page.Readme) != 1 || page.Readme[0].Name != "readme.MD" {
			t.Fatalf("README independent of page: %+v", page.Readme)
		}
		for _, entry := range page.Entries {
			if seen[entry.Path] {
				t.Fatalf("duplicate entry %q", entry.Path)
			}
			seen[entry.Path] = true
		}
		if round == 0 {
			commitTree(t, manager, work, "main", map[string]string{"new.txt": "after push\n", "f0000.txt": "changed\n"})
		}
		if page.After == "" {
			if len(seen) != page.Total || seen["new.txt"] {
				t.Fatalf("continuation followed the new tip: %d of %d", len(seen), page.Total)
			}
			break
		}
		after = page.After
	}
	view, page, err := manager.PathPageAt(context.Background(), "sample", oid, "readme.MD", "")
	if err != nil || view.Folder || view.File.Path != "readme.MD" || len(page.Entries) != DirectoryPageEntries {
		t.Fatalf("exact file outside the first page: %+v entries=%d error=%v", view, len(page.Entries), err)
	}
	view, page, err = manager.PathPageAt(context.Background(), "sample", oid, "[literal]", "")
	if err != nil || !view.Folder || len(page.Entries) != 1 || page.Entries[0].Path != "[literal]/x.txt" {
		t.Fatalf("literal folder: %+v %+v %v", view, page, err)
	}
}
