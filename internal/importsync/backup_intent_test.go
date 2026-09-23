package importsync

import (
	"context"
	"path/filepath"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/recovery"
	"owngit/internal/state"
)

// initialIntentStatus returns the one publication intent of the named
// repository from the portable snapshot view of the store.
func portableImportIntents(t *testing.T, store *state.Store, repositoryID string) []state.ImportIntent {
	t.Helper()
	snapshot, err := store.RecoverySnapshot(context.Background())
	if err != nil {
		t.Fatalf("portable snapshot: %v", err)
	}
	var intents []state.ImportIntent
	for _, intent := range snapshot.ImportIntents {
		if intent.RepositoryID == repositoryID {
			intents = append(intents, intent)
		}
	}
	return intents
}

func completeFixtureSetup(t *testing.T, f *fixture) {
	t.Helper()
	hash, err := auth.HashPassword("backup-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CompleteSetup(context.Background(), f.manager.RepositoryRoot(), "open", "", hash, true); err != nil {
		t.Fatal(err)
	}
}

// An initial import that stopped after recording its publication intent and
// never created a repository must not block offline backup. The settled
// intent travels as history and restores unchanged.
func TestBackupCarriesSettledIntentOfAnInitialImportThatNeverPublished(t *testing.T) {
	f := newFixture(t)
	completeFixtureSetup(t, f)
	f.commit("one", "one\n")
	f.service.whileRefsPrepared = func() {
		if _, err := f.service.Cancel(context.Background(), "project"); err != nil {
			t.Errorf("cancel during prepared publication: %v", err)
		}
	}
	if _, err := f.importProject(ImportInput{}); problemCode(err) != CodeCancelled {
		t.Fatalf("cancelled initial import err=%v", err)
	}
	f.service.whileRefsPrepared = nil
	// A restart reconciles what the stopped run left behind.
	if err := f.service.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, exists, err := f.manager.ExistingPath(context.Background(), "project"); err != nil || exists {
		t.Fatalf("cancelled initial import created a repository exists=%v err=%v", exists, err)
	}
	intents := portableImportIntents(t, f.store, "project")
	if len(intents) != 1 || (intents[0].Status != state.ImportIntentNotApplied && intents[0].Status != state.ImportIntentInvalidated) {
		t.Fatalf("settled initial intents=%+v", intents)
	}
	settled := intents[0].Status

	output := filepath.Join(f.root, "backup")
	if err := recovery.Create(context.Background(), f.store, f.manager, output); err != nil {
		t.Fatalf("backup refused a settled intent without a repository: %v", err)
	}
	restoredState := filepath.Join(f.root, "restored-state")
	if err := recovery.Restore(context.Background(), output, restoredState, filepath.Join(f.root, "restored-repositories"), f.gitPath); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored, err := state.Open(context.Background(), restoredState)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	after := portableImportIntents(t, restored, "project")
	if len(after) != 1 || after[0].ID != intents[0].ID || after[0].Status != settled {
		t.Fatalf("restored intents=%+v want %s %s", after, intents[0].ID, settled)
	}
	if count, err := restored.UnresolvedImportIntentCount(context.Background(), "project"); err != nil || count != 0 {
		t.Fatalf("restored unresolved count=%d err=%v", count, err)
	}
}

// portableImportIntentsAllowingFailure reads intents directly, because the
// portable snapshot itself refuses an unresolved intent without a repository.
func portableImportIntentsAllowingFailure(store *state.Store, repositoryID string) []state.ImportIntent {
	intents, _ := store.PendingImportIntents(context.Background(), repositoryID)
	return intents
}

// An owner-resolved intent is portable history: backup carries it with its
// receipt, and restore keeps it terminal instead of reopening it.
func TestBackupCarriesOwnerResolvedIntent(t *testing.T) {
	f, _, _ := unresolvedHEADRefresh(t)
	completeFixtureSetup(t, f)
	ctx := context.Background()
	result, err := f.service.ResolveUnresolved(ctx, "project")
	if err != nil || len(result.Resolved) != 1 {
		t.Fatalf("resolve result=%+v err=%v", result, err)
	}
	resolved, _, err := f.store.ImportIntent(ctx, result.Resolved[0])
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(f.root, "backup")
	if err := recovery.Create(ctx, f.store, f.manager, output); err != nil {
		t.Fatalf("backup with an owner-resolved intent: %v", err)
	}
	restoredState := filepath.Join(f.root, "restored-state")
	if err := recovery.Restore(ctx, output, restoredState, filepath.Join(f.root, "restored-repositories"), f.gitPath); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored, err := state.Open(ctx, restoredState)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	intent, exists, err := restored.ImportIntent(ctx, resolved.ID)
	if err != nil || !exists || intent.Status != state.ImportIntentOwnerResolved || intent.ReceiptJSON != resolved.ReceiptJSON || intent.Reason != resolved.Reason {
		t.Fatalf("restored intent=%+v exists=%v err=%v", intent, exists, err)
	}
	if count, err := restored.UnresolvedImportIntentCount(ctx, "project"); err != nil || count != 0 {
		t.Fatalf("restored unresolved count=%d err=%v", count, err)
	}
}
