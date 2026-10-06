package importsync

import (
	"context"
	"testing"
)

func TestSourceDeletionCountIncludesKeptAndRemovedRefs(t *testing.T) {
	for _, follow := range []bool{false, true} {
		name := "kept"
		if follow {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.commit("initial", "initial\n")
			f.git(f.source, "branch", "old")
			f.git(f.source, "branch", "local")
			f.git(f.source, "tag", "old")
			f.mustImport(ImportInput{})
			owner := f.localWork("local", "owner work\n")
			f.git(f.source, "branch", "-D", "old", "local")
			f.git(f.source, "tag", "-d", "old")
			_, err := f.service.ChangeOptions(context.Background(), "project", OptionsChange{FollowUpstreamDeletions: boolPointer(follow)})
			noErr(t, err)
			run, err := f.refresh()
			noErr(t, err)
			eq(t, "source deletion count", run.RefsDeletedUpstream, 3)
			refs := f.destinationRefs()
			eq(t, "independent owner work", refs["refs/heads/local"], owner)
			for _, name := range []string{"refs/heads/old", "refs/tags/old"} {
				require(t, (refs[name] == "") == follow,
					"wrong local deletion policy: %s=%s follow=%v", name, refs[name], follow)
			}
		})
	}
}
