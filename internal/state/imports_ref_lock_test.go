package state

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnresolvedHEADOwnershipIsNotExportedOrRestored(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	noErr(t, store.AddRepository(ctx, Repository{ID: "project", Name: "Project", CreatedAt: testImportNow()}))
	configureTestImportSource(t, store, "project")
	run := beginTestImportRun(t, store, strings.Repeat("b", 32), "project", ImportKindRefresh, ImportRunPublishing)
	head := "symbolic refs/heads/main " + strings.Repeat("a", 40)
	intent := ImportIntent{ID: strings.Repeat("c", 32), RepositoryID: "project", RunID: run.ID, SourceGeneration: 1, AuthorityRevision: 1,
		Status: ImportIntentPlanning, Expected: map[string]string{ImportHeadRef: head}, Desired: map[string]string{ImportHeadRef: head},
		Observed: map[string]string{ImportHeadRef: head}, Retained: map[string]string{}, CreatedAt: testImportNow()}
	noErr(t, store.CreateImportIntent(ctx, intent))
	noErr(t, store.UpdateImportIntentHEADOwnership(ctx, intent.ID, ImportIntentApplied, "", testImportNow()))
	noErr(t, store.UpdateImportIntent(ctx, intent.ID, ImportIntentUnresolved, "", "", "inspection unavailable", testImportNow()))
	run.Status, run.FinishedAt = ImportRunUnresolved, testImportNow()
	noErr(t, store.FinishImportRun(ctx, run))
	local, exists, err := store.ImportIntent(ctx, intent.ID)
	noErr(t, err)
	if !exists || !local.HeadOwned {
		t.Fatal("local historical write proof was lost")
	}
	snapshot, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	if len(snapshot.ImportIntents) != 1 || snapshot.ImportIntents[0].HeadOwned {
		t.Error("unfinished HEAD ownership was exported")
	}
	// Restore must also settle snapshots made without the exporter guard.
	snapshot.ImportIntents[0].HeadOwned = true
	restored := openTestStore(t)
	noErr(t, restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot))
	copy, exists, err := restored.ImportIntent(ctx, intent.ID)
	noErr(t, err)
	if !exists || copy.HeadOwned || copy.Status != ImportIntentUnresolved {
		t.Errorf("unfinished ownership restored: %+v", copy)
	}
	local, exists, err = store.ImportIntent(ctx, intent.ID)
	noErr(t, err)
	if !exists || !local.HeadOwned {
		t.Fatal("portable settlement altered the serving copy")
	}
}

func TestImportRefLockEvidenceSerializationIsBounded(t *testing.T) {
	store := openTestStore(t)
	proof := ImportRefLock{ID: strings.Repeat("a", 32), RepositoryID: "project", RunID: strings.Repeat("b", 32),
		SessionID: strings.Repeat("c", 32), RepositoryPath: filepath.Join(t.TempDir(), strings.Repeat("\x01", 3500)),
		Name: "HEAD", Content: strings.Repeat("\x01", 2048), Size: 2048}
	noErr(t, validateImportRefLock(proof))
	if err := store.SaveImportRefLock(context.Background(), proof); err == nil {
		t.Fatal("oversized serialized evidence was stored")
	}
	records, err := store.ImportRefLocksPage(context.Background(), "", 100)
	noErr(t, err)
	if len(records) != 0 {
		t.Fatal("refused evidence was persisted")
	}
}

func TestImportRefLockEvidenceStaysMachineLocal(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	proof := ImportRefLock{ID: strings.Repeat("a", 32), RepositoryID: "project", RunID: strings.Repeat("b", 32),
		SessionID: strings.Repeat("c", 32), RepositoryPath: filepath.Join(t.TempDir(), "project.git"), Name: "HEAD",
		DirectoryID: "unix:1:2", FileID: "unix:1:3", Content: "ref: refs/heads/private-machine-marker\n", Size: 37, ModTime: 123456789}
	proof.Size = int64(len(proof.Content))
	noErr(t, store.SaveImportRefLock(ctx, proof))
	records, err := store.ImportRefLocksPage(ctx, "", 100)
	noErr(t, err)
	if len(records) != 1 || records[0] != proof {
		t.Fatalf("proof round trip: %+v", records)
	}
	snapshot, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	encoded, err := json.Marshal(snapshot)
	noErr(t, err)
	for _, forbidden := range []string{proof.Content, proof.RepositoryPath, "private-machine-marker", importRefLockPrefix} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("machine-local evidence was exported: %s", forbidden)
		}
	}
	restored := openTestStore(t)
	noErr(t, restored.RestoreRecoveryState(ctx, t.TempDir(), snapshot))
	records, err = restored.ImportRefLocksPage(ctx, "", 100)
	noErr(t, err)
	if len(records) != 0 {
		t.Fatalf("restored machine authority: %+v", records)
	}
	// Snapshotting must not revoke the serving process's local recovery evidence.
	records, err = store.ImportRefLocksPage(ctx, "", 100)
	noErr(t, err)
	if len(records) != 1 {
		t.Fatal("snapshot changed serving lock evidence")
	}
	noErr(t, store.DeleteImportRefLock(ctx, proof.ID))
	records, err = store.ImportRefLocksPage(ctx, "", 100)
	noErr(t, err)
	if len(records) != 0 {
		t.Fatal("settled evidence was not removed")
	}
}
