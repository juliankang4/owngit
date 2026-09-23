package importsync

import (
	"reflect"
	"testing"
)

func TestParentUnsupportedSymbolicHEADIsNotDetached(t *testing.T) {
	for _, target := range []string{"refs/tags/v1", "refs/remotes/origin/main"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			oid := f.commit("source", "source bytes\n")
			f.git(f.source, "update-ref", target, oid)
			f.git(f.source, "symbolic-ref", "HEAD", target)
			before := f.sourceRefs()
			result, err := f.importProject(ImportInput{})
			if err == nil {
				actual := f.gitMaybe(f.destinationPath(), "--git-dir", ".", "symbolic-ref", "-q", "HEAD")
				t.Fatalf("unsupported symbolic HEAD silently imported: source=%s destination=%q status=%s", target, actual, result.Run.Status)
			}
			assertImportDestinationAbsent(t, f)
			if after := f.sourceRefs(); !reflect.DeepEqual(before, after) {
				t.Fatalf("source refs changed: before=%v after=%v", before, after)
			}
			if got := f.git(f.source, "symbolic-ref", "HEAD"); got != target {
				t.Fatalf("source HEAD changed: got=%s want=%s", got, target)
			}
		})
	}
}
