package importsync

import "testing"

// Different annotations are different local tag history, even when both tags
// peel to the same commit. Branch ancestry must not erase that local change.
func TestParentRefreshPreservesLocallyReannotatedTag(t *testing.T) {
	f := newFixture(t)
	commit := f.commit("synthetic upstream", "one\n")
	f.git(f.source, "tag", "-a", "v1", "-m", "upstream annotation", commit)
	upstreamTag := f.git(f.source, "rev-parse", "refs/tags/v1")
	f.mustImport(ImportInput{})
	destination := f.destinationPath()
	if got := f.git(destination, "rev-parse", "refs/tags/v1"); got != upstreamTag {
		t.Fatal("initial tag import was not exact")
	}
	f.git(destination, "tag", "-f", "-a", "v1", "-m", "independent local annotation", commit)
	localTag := f.git(destination, "rev-parse", "refs/tags/v1")
	if localTag == upstreamTag {
		t.Fatal("test did not create distinct local tag history")
	}
	if _, err := f.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := f.git(f.source, "rev-parse", "refs/tags/v1"); got != upstreamTag {
		t.Fatal("upstream was unexpectedly modified")
	}
	if got := f.git(destination, "rev-parse", "refs/tags/v1"); got != localTag {
		t.Fatalf("refresh overwrote independent local annotation: got %s want %s", got, localTag)
	}
}
