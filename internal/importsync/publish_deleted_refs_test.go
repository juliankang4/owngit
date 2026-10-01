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
			if run.RefsDeletedUpstream != 3 {
				t.Fatalf("source deletion count=%d", run.RefsDeletedUpstream)
			}
			refs := f.destinationRefs()
			if refs["refs/heads/local"] != owner {
				t.Fatal("independent owner work was deleted")
			}
			for _, name := range []string{"refs/heads/old", "refs/tags/old"} {
				if (refs[name] == "") != follow {
					t.Fatalf("wrong local deletion policy: %s=%s follow=%v", name, refs[name], follow)
				}
			}
		})
	}
}
