package importsync

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"owngit/internal/importgit"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// longBranchName returns a multi-component branch name of exactly size bytes,
// keeping every path component within filesystem name limits.
func longBranchName(size int) string {
	name := "refs/heads/"
	for size-len(name) > 200 {
		name += strings.Repeat("a", 199) + "/"
	}
	return name + strings.Repeat("b", size-len(name))
}

// The longest accepted branch name still yields a provenance retention name
// that state accepts, at the SHA-256 width where the derived name is longest.
func TestLongestSelectedBranchKeepsRetentionWithinStateBound(t *testing.T) {
	f := newFixture(t)
	if err := os.RemoveAll(f.source); err != nil {
		t.Fatal(err)
	}
	f.format = "sha256"
	f.initSource()
	// Windows needs long path support in the fixture's own source repository;
	// the product runner already enables it for OwnGit's Git commands.
	f.git(f.source, "config", "core.longpaths", "true")
	first := f.commit("one", "one\n")
	name := longBranchName(maxSelectedRefNameBytes)
	f.git(f.source, "update-ref", name, first)
	if result := f.mustImport(ImportInput{}); result.Run.Status != state.ImportRunComplete {
		t.Fatalf("initial run=%+v", result.Run)
	}
	// Replace the long branch with unrelated history, so the refresh retains
	// the old tip under its provenance name.
	rewritten := f.git(f.source, "commit-tree", "-m", "unrelated", first+"^{tree}")
	f.git(f.source, "update-ref", name, rewritten)
	run, err := f.refresh()
	if err != nil || run.Status != state.ImportRunComplete || run.RefsUpdated != 1 {
		t.Fatalf("refresh run=%+v err=%v", run, err)
	}
	provenance := repository.ProvenanceRefName("heads", strings.TrimPrefix(name, "refs/heads/"), first)
	if len(provenance) != state.MaxImportRefNameBytes {
		t.Fatalf("provenance name has %d bytes; the bound is no longer tight", len(provenance))
	}
	refs := f.destinationRefs()
	if refs[name] != rewritten || refs[provenance] != first {
		t.Fatalf("long branch=%s provenance=%s", refs[name], refs[provenance])
	}
}

// A longer upstream branch is refused as an unsupported ref before any pack is
// indexed, instead of failing late as a state error after the transfer. A
// 501-byte name used to fail every run when the intent was recorded; a
// 418-byte name used to publish and then fail once a rewrite needed its
// provenance name. Both are now refused at selection with a clear message.
func TestOverlongSelectedRefIsRefusedBeforeIndexing(t *testing.T) {
	for _, size := range []int{maxSelectedRefNameBytes + 1, state.MaxImportRefNameBytes + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			f := newFixture(t)
			f.git(f.source, "config", "core.longpaths", "true")
			first := f.commit("one", "one\n")
			f.mustImport(ImportInput{})
			path := f.destinationPath()
			before := f.destinationRefs()

			name := longBranchName(size)
			f.git(f.source, "update-ref", name, f.commit("two", "two\n"))
			run, err := f.refresh()
			if code := problemCode(err); err == nil || code != CodeUnsupportedRefs {
				t.Fatalf("refresh code=%s err=%v", code, err)
			}
			if run.Status != state.ImportRunFailed || run.ErrorClass != CodeUnsupportedRefs || !strings.Contains(run.Message, strconv.Itoa(size)+" bytes") {
				t.Fatalf("refresh run=%+v", run)
			}
			stored := f.lastRun()
			if stored.ID != run.ID || stored.ErrorClass != CodeUnsupportedRefs {
				t.Fatalf("stored run=%+v", stored)
			}
			if after := f.destinationRefs(); len(after) != len(before) || after["refs/heads/main"] != first {
				t.Fatalf("destination changed: before=%v after=%v", before, after)
			}
			if f.gitMaybe(path, "cat-file", "-t", f.git(f.source, "rev-parse", "refs/heads/main")) != "" {
				t.Fatal("refused refresh indexed the new source commit into the destination")
			}
		})
	}
}

// The run record stores the source HEAD target, so an over-long target is
// refused as an unsupported ref and the refusal itself is recorded.
func TestOverlongSourceHEADTargetIsRefused(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.mustImport(ImportInput{})
	target := longBranchName(maxSelectedRefNameBytes + 1)
	f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
		advertisement.Head.SymrefTarget = target
	}
	run, err := f.refresh()
	if code := problemCode(err); code != CodeUnsupportedRefs {
		t.Fatalf("refresh code=%s err=%v", code, err)
	}
	stored := f.lastRun()
	if stored.ID != run.ID || stored.Status != state.ImportRunFailed || stored.ErrorClass != CodeUnsupportedRefs || stored.HeadSymref != "" {
		t.Fatalf("stored run=%+v", stored)
	}
}
