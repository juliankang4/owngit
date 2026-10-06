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
	eq(t, "initial tag import", f.git(destination, "rev-parse", "refs/tags/v1"), upstreamTag)
	f.git(destination, "tag", "-f", "-a", "v1", "-m", "independent local annotation", commit)
	localTag := f.git(destination, "rev-parse", "refs/tags/v1")
	require(t, localTag != upstreamTag, "test did not create distinct local tag history")
	_, err := f.refresh()
	noErr(t, err)
	eq(t, "upstream tag", f.git(f.source, "rev-parse", "refs/tags/v1"), upstreamTag)
	eq(t, "local annotation after refresh", f.git(destination, "rev-parse", "refs/tags/v1"), localTag)
}
