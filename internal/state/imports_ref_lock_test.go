package state

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

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
