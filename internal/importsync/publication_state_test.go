package importsync

import (
	"context"
	"strings"
	"testing"

	"owngit/internal/state"
)

func TestLegacyIntentWithoutExactHEADFactsFailsClosed(t *testing.T) {
	f := newFixture(t)
	oid := f.commit("initial", "initial\n")
	f.mustImport(ImportInput{})
	run := state.ImportRun{
		ID: strings.Repeat("e", 32), RepositoryID: "project", SourceGeneration: 1, AuthorityRevision: 1,
		Kind: state.ImportKindRefresh, Status: state.ImportRunPreparing, StartedAt: f.now, CreatedAt: f.now,
	}
	noErr(t, f.store.BeginImportRun(context.Background(), run))
	run.Status = state.ImportRunInterrupted
	run.FinishedAt = f.now
	noErr(t, f.store.FinishImportRun(context.Background(), run))
	intent := state.ImportIntent{
		ID: strings.Repeat("f", 32), RepositoryID: "project", RunID: run.ID,
		SourceGeneration: 1, AuthorityRevision: 1, Status: state.ImportIntentPlanning,
		Expected: map[string]string{"refs/heads/main": oid},
		Desired:  map[string]string{"refs/heads/main": oid},
		Observed: map[string]string{"refs/heads/main": oid},
		Retained: map[string]string{}, CreatedAt: f.now,
	}
	noErr(t, f.store.CreateImportIntent(context.Background(), intent))
	if err := f.service.Reconcile(context.Background()); err == nil || problemCode(err) != CodeUnresolved {
		t.Fatalf("legacy intent did not fail closed: %v", err)
	}
	stored, exists, err := f.store.ImportIntent(context.Background(), intent.ID)
	if err != nil || !exists || stored.Status != state.ImportIntentUnresolved || stored.ReceiptJSON != "" {
		t.Fatalf("legacy intent=%+v exists=%v err=%v", stored, exists, err)
	}
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
			if err := f.store.Exec(context.Background(), `UPDATE import_ref_observations SET run_id='' WHERE repository_id=? AND source_generation=? AND ref_name=?`,
				observation.RepositoryID, observation.SourceGeneration, observation.RefName); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.git(f.source, "checkout", "--quiet", "dev")
	run, err := f.refresh()
	noErr(t, err)
	if run.RefsDivergent != 1 {
		t.Fatalf("unrelated HEAD observation established ownership: %+v", run)
	}
	if got := f.git(f.destinationPath(), "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("destination HEAD changed through unjoined observation: %s", got)
	}
}
