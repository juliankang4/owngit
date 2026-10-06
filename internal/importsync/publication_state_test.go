package importsync

import (
	"context"
	"testing"

	"owngit/internal/state"
)

func TestLegacyIntentWithoutExactHEADFactsFailsClosed(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	refs := map[string]string{"refs/heads/main": oid}
	intent := f.seedIntent('e', refs, refs, refs)
	err := f.service.Reconcile(context.Background())
	require(t, err != nil && problemCode(err) == CodeUnresolved, "legacy intent did not fail closed: %v", err)
	stored, exists, err := f.store.ImportIntent(context.Background(), intent.ID)
	require(t, err == nil && exists && stored.Status == state.ImportIntentUnresolved && stored.ReceiptJSON == "",
		"legacy intent=%+v exists=%v err=%v", stored, exists, err)
}

func TestIntentObservationRequiresRelatedCompleteRunForHEADOwnership(t *testing.T) {
	f := newFixture(t)
	f.commit("initial", "initial\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	observations, err := f.store.ImportObservations(context.Background(), "project", 1)
	noErr(t, err)
	for _, observation := range observations {
		if observation.RefName == state.ImportHeadRef {
			noErr(t, f.store.Exec(context.Background(), `UPDATE import_ref_observations SET run_id='' WHERE repository_id=? AND source_generation=? AND ref_name=?`,
				observation.RepositoryID, observation.SourceGeneration, observation.RefName))
		}
	}
	f.git(f.source, "checkout", "--quiet", "dev")
	run, err := f.refresh()
	noErr(t, err)
	eq(t, "divergent refs (an unrelated HEAD observation must not establish ownership)", run.RefsDivergent, 1)
	eq(t, "destination HEAD", f.git(f.destinationPath(), "symbolic-ref", "HEAD"), "refs/heads/main")
}
