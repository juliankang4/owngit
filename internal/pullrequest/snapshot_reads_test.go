package pullrequest

import "testing"

func TestPullRequestWritesRefreshSnapshotDerivedReads(t *testing.T) {
	fixture := newServiceFixture(t)
	target := fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	source := fixture.commitFile("feature.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	observe := func(want string) {
		t.Helper()
		snapshot, err := fixture.manager.RefSnapshot(fixture.ctx, fixture.repositoryID)
		noErr(t, err)
		tips, err := fixture.manager.RefTipsAt(fixture.ctx, fixture.repositoryID, snapshot, false)
		noErr(t, err)
		if snapshot.Summary.DefaultOID != want || tips["main"].OID != want {
			t.Fatalf("listing=%s tip=%s, want %s", snapshot.Summary.DefaultOID, tips["main"].OID, want)
		}
		_, err = fixture.manager.RetainedRefsAt(fixture.ctx, fixture.repositoryID, snapshot)
		noErr(t, err)
	}
	observe(target)
	created, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Feature", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip"})
	noErr(t, err)
	observe(target)
	merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: source, TargetOID: target})
	noErr(t, err)
	if merged.Merge == nil {
		t.Fatal("merge has no recorded result")
	}
	observe(merged.Merge.OID)
}
