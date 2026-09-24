package importsync

import (
	"context"
	"testing"

	"owngit/internal/state"
)

// TestImportPublicationRefreshesTheRefSnapshot proves that a ref snapshot
// read after an import publication shows the published refs, although the
// snapshot of the earlier refs is cached.
func TestImportPublicationRefreshesTheRefSnapshot(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	ctx := context.Background()
	imported, err := f.manager.RefSnapshot(ctx, "project")
	noErr(t, err, "snapshot after import")
	if imported.Summary.DefaultOID != f.sourceRefs()["refs/heads/main"] {
		t.Fatalf("snapshot after import=%+v", imported.Summary)
	}

	second := f.commit("two", "two\n")
	f.git(f.source, "tag", "v2")
	run, err := f.refresh()
	noErr(t, err, "refresh")
	if run.Status != state.ImportRunComplete {
		t.Fatalf("refresh run=%+v", run)
	}
	refreshed, err := f.manager.RefSnapshot(ctx, "project")
	noErr(t, err, "snapshot after refresh")
	if refreshed.Summary.DefaultOID != second || refreshed.Head.OID != second || refreshed.Head.Subject != "two" {
		t.Fatalf("snapshot after refresh=%+v head=%+v, want main at %s", refreshed.Summary, refreshed.Head, second)
	}
	if len(refreshed.Summary.Tags) != 1 || refreshed.Summary.Tags[0].Name != "v2" {
		t.Fatalf("snapshot after refresh tags=%+v, want v2", refreshed.Summary.Tags)
	}
}
