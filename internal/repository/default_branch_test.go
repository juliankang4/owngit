package repository

import (
	"context"
	"testing"
)

func TestDeletingDefaultAndLastBranchIsReportedWithoutRecreation(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	old := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", ":refs/heads/main")

	summary, err := manager.Summary(context.Background(), "sample")
	noErr(t, err)
	if summary.DefaultBranch != "main" || summary.DefaultOID != "" || summary.Empty {
		t.Fatalf("deleted default branch was misreported: %+v", summary)
	}
	if len(summary.Branches) != 0 {
		t.Fatalf("default branch was recreated: %+v", summary.Branches)
	}
	assertMissingRef(t, remote, "refs/heads/main")
	assertRef(t, remote, "refs/owngit/retained/heads/"+old, old)
}
