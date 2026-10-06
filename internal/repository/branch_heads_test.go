package repository

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// BranchHeads lists what Summary lists for branches and, like Summary, fails
// on a branch whose object is missing.
func TestBranchHeadsMatchSummaryAndFailOnMissingObject(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	ctx := context.Background()
	commitFile(t, work, "one", "one", "2024-01-01T10:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/other")
	heads, _, _, err := manager.BranchHeads(ctx, "sample", nil)
	noErr(t, err)
	summary, err := manager.Summary(ctx, "sample")
	noErr(t, err)
	if !reflect.DeepEqual(heads, summary.Branches) {
		t.Fatalf("BranchHeads=%+v, Summary branches=%+v", heads, summary.Branches)
	}
	noErr(t, os.WriteFile(filepath.Join(remote, "refs", "heads", "missing"), []byte("ffffffffffffffffffffffffffffffffffffffff\n"), 0o600))
	if _, _, _, err := manager.BranchHeads(ctx, "sample", nil); err == nil {
		t.Fatal("BranchHeads accepted a branch whose object is missing")
	}
	if _, err := manager.Summary(ctx, "sample"); err == nil {
		t.Fatal("Summary accepted a branch whose object is missing")
	}
}
