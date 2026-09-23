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

// An upstream ref that differs only by case from a packed local ref is left
// uncreated. On a case-insensitive filesystem its loose file would otherwise
// answer for the packed local branch and silently replace it.
func TestUpstreamCaseVariantOfPackedLocalRefStaysDivergent(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	f.git(f.source, "branch", "feature")
	f.mustImport(ImportInput{})
	path := f.destinationPath()
	local := f.git(path, "rev-parse", "refs/heads/feature")
	// Operator maintenance packs every ref; auto gc is disabled, so this is
	// the only compaction path.
	f.git(path, "pack-refs", "--all")
	if _, err := os.Stat(filepath.Join(path, "refs", "heads", "feature")); !os.IsNotExist(err) {
		t.Fatalf("feature is still loose after pack-refs: %v", err)
	}

	f.git(f.source, "checkout", "-q", "feature")
	upstream := f.commit("two", "two\n")
	f.git(f.source, "checkout", "-q", "main")
	renameAdvertisedRef(f, "refs/heads/feature", "refs/heads/Feature")

	run, err := f.refresh()
	if err != nil || run.Status != state.ImportRunComplete {
		t.Fatalf("refresh run=%+v err=%v", run, err)
	}
	if run.RefsDivergent != 1 || run.RefsCreated != 0 {
		t.Fatalf("refresh counts created=%d divergent=%d", run.RefsCreated, run.RefsDivergent)
	}
	if got := f.git(path, "rev-parse", "refs/heads/feature"); got != local {
		t.Fatalf("local feature now resolves to %s, want %s (upstream %s)", got, local, upstream)
	}
	listed := f.git(path, "for-each-ref", "--format=%(refname)", "refs/heads")
	if strings.Contains(listed, "refs/heads/Feature") {
		t.Fatalf("case variant was created: %q", listed)
	}
	if _, err := os.Stat(filepath.Join(path, "refs", "heads", "Feature")); !os.IsNotExist(err) {
		t.Fatalf("a loose ref file answers for Feature: %v", err)
	}
	// Crash reconciliation rebuilds the counts from the recorded intent and
	// must describe the case-blocked ref as the normal path did.
	intent, exists, err := f.store.CompletedImportIntentForRun(context.Background(), run.ID)
	if err != nil || !exists {
		t.Fatalf("completed intent exists=%v err=%v", exists, err)
	}
	var reconciled state.ImportRun
	completeRunFromIntent(&reconciled, intent, f.now)
	if reconciled.RefsCreated != run.RefsCreated || reconciled.RefsUpdated != run.RefsUpdated ||
		reconciled.RefsUnchanged != run.RefsUnchanged || reconciled.RefsDivergent != run.RefsDivergent {
		t.Fatalf("reconciled counts=%+v normal counts=%+v", reconciled, run)
	}
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
	if run.RefsUnchanged != 1 || run.RefsDivergent != 1 || run.RefsCreated != 0 || run.RefsDeletedUpstream != 0 {
		t.Fatalf("reconciled counts unchanged=%d created=%d divergent=%d deletedUpstream=%d",
			run.RefsUnchanged, run.RefsCreated, run.RefsDivergent, run.RefsDeletedUpstream)
	}
}

// When the source HEAD names such a variant, an owned destination HEAD stays
// local instead of pointing at a ref that was not created.
func TestOwnedHEADDoesNotFollowCaseBlockedTarget(t *testing.T) {
	f := newFixture(t)
	f.commit("one", "one\n")
	initial := f.mustImport(ImportInput{})
	markHEADOwnedForTest(t, f, initial.Run.ID)
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
	if err != nil || run.Status != state.ImportRunComplete {
		t.Fatalf("refresh run=%+v err=%v", run, err)
	}
	if head := f.git(path, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("destination HEAD=%s", head)
	}
	if got := f.git(path, "rev-parse", "refs/heads/main"); got == upstream {
		t.Fatal("local main was replaced through its case variant")
	}
}
