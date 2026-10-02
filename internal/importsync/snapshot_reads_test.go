package importsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestImportRefreshesSnapshotDerivedReads(t *testing.T) {
	f := newFixture(t)
	old := f.commit("old root", "old\n")
	f.mustImport(ImportInput{})
	ctx := context.Background()
	observe := func(want string, retained int) {
		t.Helper()
		snapshot, err := f.manager.RefSnapshot(ctx, "project")
		noErr(t, err, "snapshot")
		tips, err := f.manager.RefTipsAt(ctx, "project", snapshot, false)
		noErr(t, err, "tips")
		kept, err := f.manager.RetainedRefsAt(ctx, "project", snapshot)
		noErr(t, err, "retained")
		if snapshot.Summary.DefaultOID != want || tips["main"].OID != want || len(kept) != retained {
			t.Fatalf("listing=%s tip=%s retained=%d, want %s and %d", snapshot.Summary.DefaultOID, tips["main"].OID, len(kept), want, retained)
		}
	}
	observe(old, 0)
	noErr(t, os.WriteFile(filepath.Join(f.source, "file.txt"), []byte("rewritten\n"), 0o600), "rewrite")
	f.git(f.source, "add", "file.txt")
	f.git(f.source, "commit", "--amend", "-m", "new root")
	fresh := f.git(f.source, "rev-parse", "HEAD")
	_, err := f.refresh()
	noErr(t, err, "refresh")
	observe(fresh, 1)
}
