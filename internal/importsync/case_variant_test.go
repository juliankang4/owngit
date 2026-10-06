package importsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/importgit"
	"owngit/internal/state"
)

// renameAdvertisedRef makes the fake source advertise one ref under another
// name, as a case-only upstream rename does, without depending on how the
// test filesystem treats case.
func renameAdvertisedRef(f *fixture, from, to string) {
	f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
		for index := range advertisement.Refs {
			if advertisement.Refs[index].Name == from {
				advertisement.Refs[index].Name = to
			}
		}
	}
}

type nameVariantArrange func(t *testing.T, f *fixture, path *string) (check func(listed string))

// advanceBranch moves branch one commit ahead in the source.
func advanceBranch(f *fixture, branch string) {
	f.git(f.source, "checkout", "-q", branch)
	f.commit("two", "two\n")
	f.git(f.source, "checkout", "-q", "main")
}

// checkNameVariant runs a refresh after arrange (which imports, packs every
// ref as git pack-refs --all leaves them, so a loose file cannot answer for a
// packed ref, and changes the source) and checks the shared outcome: the
// refresh completes, nothing is created or updated, one ref is divergent, and
// crash reconciliation rebuilds the same counts from the recorded intent.
func checkNameVariant(t *testing.T, arrange nameVariantArrange) {
	f := newFixture(t)
	f.commit("one", "one\n")
	var path string
	check := arrange(t, f, &path)
	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete, "refresh run=%+v err=%v", run, err)
	require(t, run.RefsDivergent == 1 && run.RefsCreated == 0 && run.RefsUpdated == 0,
		"refresh counts created=%d updated=%d divergent=%d", run.RefsCreated, run.RefsUpdated, run.RefsDivergent)
	check(f.git(path, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"))
	intent, exists, err := f.store.CompletedImportIntentForRun(context.Background(), run.ID)
	require(t, err == nil && exists, "completed intent exists=%v err=%v", exists, err)
	var reconciled state.ImportRun
	completeRunFromIntent(&reconciled, intent, f.now)
	require(t, reconciled.RefsCreated == run.RefsCreated && reconciled.RefsUpdated == run.RefsUpdated &&
		reconciled.RefsUnchanged == run.RefsUnchanged && reconciled.RefsDivergent == run.RefsDivergent,
		"reconciled counts=%+v normal counts=%+v", reconciled, run)
}

// An upstream ref whose name a file system would treat as another destination
// ref (letter case, ß and ss) is left uncreated, and a destination pair that
// already shares such a name keeps its local values, by the rule pushes
// follow (repository.RefNameKey).
func TestUpstreamNameVariantsOfPackedLocalRefsStayDivergent(t *testing.T) {
	for _, test := range []struct {
		name    string
		arrange nameVariantArrange
	}{
		{"case variant", func(t *testing.T, f *fixture, path *string) func(string) {
			f.git(f.source, "branch", "feature")
			f.mustImport(ImportInput{})
			*path = f.destinationPath()
			local := f.git(*path, "rev-parse", "refs/heads/feature")
			f.git(*path, "pack-refs", "--all")
			_, err := os.Stat(filepath.Join(*path, "refs", "heads", "feature"))
			require(t, os.IsNotExist(err), "feature is still loose after pack-refs: %v", err)
			advanceBranch(f, "feature")
			renameAdvertisedRef(f, "refs/heads/feature", "refs/heads/Feature")
			return func(listed string) {
				eq(t, "local feature", f.git(*path, "rev-parse", "refs/heads/feature"), local)
				require(t, !strings.Contains(listed, "refs/heads/Feature"), "case variant was created: %q", listed)
				_, err := os.Stat(filepath.Join(*path, "refs", "heads", "Feature"))
				require(t, os.IsNotExist(err), "a loose ref file answers for Feature: %v", err)
			}
		}},
		{"destination refs sharing a name", func(t *testing.T, f *fixture, path *string) func(string) {
			f.git(f.source, "branch", "feature")
			f.mustImport(ImportInput{})
			*path = f.destinationPath()
			local := f.git(*path, "rev-parse", "refs/heads/feature")
			f.git(*path, "pack-refs", "--all")
			f.git(*path, "update-ref", "refs/heads/Feature", local)
			f.git(*path, "pack-refs", "--all")
			advanceBranch(f, "feature")
			return func(string) {
				listed := f.git(*path, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/feature", "refs/heads/Feature")
				eq(t, "feature and Feature", listed, "refs/heads/Feature "+local+"\nrefs/heads/feature "+local)
			}
		}},
		{"folded variant", func(t *testing.T, f *fixture, path *string) func(string) {
			f.git(f.source, "branch", "strasse")
			f.mustImport(ImportInput{})
			*path = f.destinationPath()
			local := f.git(*path, "rev-parse", "refs/heads/strasse")
			f.git(*path, "pack-refs", "--all")
			advanceBranch(f, "strasse")
			renameAdvertisedRef(f, "refs/heads/strasse", "refs/heads/stra\u00dfe")
			return func(listed string) {
				require(t, !strings.Contains(listed, "stra\u00dfe") &&
					strings.Contains(listed, "refs/heads/strasse "+local), "refs after refresh:\n%s", listed)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) { checkNameVariant(t, test.arrange) })
	}
}

// A source ref in a folder spelled like a local destination ref apart from
// letter case (topic/x beside a local branch Topic) is left uncreated.
func TestUpstreamRefInAFolderSpelledLikeALocalRefStaysDivergent(t *testing.T) {
	checkNameVariant(t, func(t *testing.T, f *fixture, path *string) func(string) {
		f.mustImport(ImportInput{})
		*path = f.destinationPath()
		local := f.git(*path, "rev-parse", "refs/heads/main")
		f.git(*path, "update-ref", "refs/heads/Topic", local)
		f.git(*path, "pack-refs", "--all")
		f.git(f.source, "branch", "topic/x")
		return func(listed string) {
			eq(t, "refs after refresh", listed, "refs/heads/Topic "+local+"\nrefs/heads/main "+local)
		}
	})
}

// A source that advertises the default branch under a spelling HFS+
// compares as equal (Georgian U+10D0 for U+10A0) does not create it beside
// the packed local branch.
func TestUpstreamHFSPlusVariantOfPackedDefaultBranchStaysDivergent(t *testing.T) {
	checkNameVariant(t, func(t *testing.T, f *fixture, path *string) func(string) {
		f.git(f.source, "branch", "\u10a0\u10a1")
		f.mustImport(ImportInput{})
		*path = f.destinationPath()
		local := f.git(*path, "rev-parse", "refs/heads/\u10a0\u10a1")
		f.git(*path, "pack-refs", "--all")
		advanceBranch(f, "\u10a0\u10a1")
		renameAdvertisedRef(f, "refs/heads/\u10a0\u10a1", "refs/heads/\u10d0\u10d1")
		return func(listed string) {
			require(t, !strings.Contains(listed, "\u10d0") && strings.Contains(listed, "refs/heads/\u10a0\u10a1 "+local),
				"refs after refresh:\n%s", listed)
		}
	})
}

// The reconciled counts for the intent shape of a case-blocked ref: observed
// upstream value, expected absence, and no desired value.
func TestReconciledCountsTreatCaseBlockedRefAsDivergent(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	intent := state.ImportIntent{
		Observed: map[string]string{"refs/heads/main": a, "refs/heads/Feature": b},
		Expected: map[string]string{"refs/heads/main": a, "refs/heads/Feature": ""},
		Desired:  map[string]string{"refs/heads/main": a},
	}
	var run state.ImportRun
	completeRunFromIntent(&run, intent, time.Unix(1, 0))
	require(t, run.RefsUnchanged == 1 && run.RefsDivergent == 1 && run.RefsCreated == 0 && run.RefsDeletedUpstream == 0,
		"reconciled counts unchanged=%d created=%d divergent=%d deletedUpstream=%d", run.RefsUnchanged, run.RefsCreated, run.RefsDivergent, run.RefsDeletedUpstream)
}

// When the source HEAD names such a variant, an owned destination HEAD stays
// local instead of pointing at a ref that was not created.
func TestOwnedHEADDoesNotFollowCaseBlockedTarget(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.importOwned()
	path := f.destinationPath()
	f.git(path, "pack-refs", "--all")

	upstream := f.commit("two", "two\n")
	renameAdvertisedRef(f, "refs/heads/main", "refs/heads/Main")
	previous := f.transport.mutateAdvertised
	f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
		previous(advertisement)
		advertisement.Head.SymrefTarget = "refs/heads/Main"
	}
	run, err := f.refresh()
	require(t, err == nil && run.Status == state.ImportRunComplete, "refresh run=%+v err=%v", run, err)
	eq(t, "destination HEAD", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/main")
	require(t, f.git(path, "rev-parse", "refs/heads/main") != upstream,
		"local main was replaced through its case variant")
}

// A source that keeps advertising main but points its HEAD at an absent
// look-alike (Main) does not move the owned HEAD: the HEAD target counts as
// a proposed name, conflicts with main, and HEAD stays on main.
func TestOwnedHEADDoesNotFollowAnUnadvertisedLookAlikeTarget(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			f := newFixture(t)
			if format == "sha256" {
				f.format = format
				f.source = filepath.Join(f.root, "source-sha256")
				f.initSource()
			}
			f.commit("one", "one\n")
			f.importOwned()
			path := f.destinationPath()
			local := f.git(path, "rev-parse", "refs/heads/main")
			f.git(path, "pack-refs", "--all")

			f.commit("two", "two\n")
			f.transport.mutateAdvertised = func(advertisement *importgit.Advertisement) {
				advertisement.Head.SymrefTarget = "refs/heads/Main"
			}
			run, err := f.refresh()
			require(t, err == nil && run.Status == state.ImportRunComplete, "refresh run=%+v err=%v", run, err)
			eq(t, "destination HEAD", f.git(path, "symbolic-ref", "HEAD"), "refs/heads/main")
			eq(t, "refs after refresh",
				f.git(path, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"), "refs/heads/main "+local)
		})
	}
}
