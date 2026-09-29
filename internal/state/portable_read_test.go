package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A backup made while an initial import publishes carries the import as
// interrupted and its intent as invalidated, as a restore would settle
// them, and leaves the running import's own records unchanged.
func TestPortableReadSettlesLiveImportOnlyInTheCopy(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, t.TempDir())
	noErr(t, err)
	defer store.Close()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	now := testImportNow()
	source := configureTestImportSource(t, store, "arriving")
	run := testImportRun(t, strings.Repeat("c", 32), "arriving", source.SourceGeneration, ImportKindInitial, ImportRunPublishing)
	run.AuthorityRevision = source.AuthorityRevision
	noErr(t, store.BeginImportRun(ctx, run))
	intent := ImportIntent{
		ID: strings.Repeat("d", 32), RepositoryID: "arriving", RunID: run.ID,
		SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision, Status: ImportIntentPlanning,
		Expected: map[string]string{"refs/heads/main": ""},
		Desired:  map[string]string{"refs/heads/main": strings.Repeat("e", 40)},
		Observed: map[string]string{"refs/heads/main": strings.Repeat("e", 40)},
		Retained: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}
	noErr(t, store.CreateImportIntent(ctx, intent))

	snapshot, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	if len(snapshot.ImportRuns) != 1 || snapshot.ImportRuns[0].Status != ImportRunInterrupted || !snapshot.ImportRuns[0].FinishedAt.Equal(run.StartedAt) {
		t.Fatalf("copied runs=%+v", snapshot.ImportRuns)
	}
	if len(snapshot.ImportIntents) != 1 || snapshot.ImportIntents[0].Status != ImportIntentInvalidated || snapshot.ImportIntents[0].HeadOwned {
		t.Fatalf("copied intents=%+v", snapshot.ImportIntents)
	}
	stored, exists, err := store.ImportRun(ctx, run.ID)
	if err != nil || !exists || stored.Status != ImportRunPublishing || stored.FinishedAt.Unix() != 0 {
		t.Fatalf("live run=%+v exists=%v err=%v", stored, exists, err)
	}
	storedIntent, exists, err := store.ImportIntent(ctx, intent.ID)
	if err != nil || !exists || storedIntent.Status != ImportIntentPlanning {
		t.Fatalf("live intent=%+v exists=%v err=%v", storedIntent, exists, err)
	}
}

// The snapshot reads on a connection of its own: while it is open, the
// store keeps writing, and the snapshot still describes its first read.
func TestPortableReadLetsTheStoreWrite(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	completeTestSetup(t, store)
	created := time.Unix(1_800_000_000, 0)
	noErr(t, store.AddRepository(ctx, Repository{ID: "before", Name: "before", CreatedAt: created}))
	read, err := store.BeginPortableRead(ctx)
	noErr(t, err)
	defer read.Close()

	written := make(chan error, 1)
	go func() {
		written <- store.AddRepository(ctx, Repository{ID: "after", Name: "after", CreatedAt: created})
	}()
	select {
	case err := <-written:
		noErr(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("a write waited for the open snapshot")
	}
	snapshot, err := read.Finish(ctx)
	noErr(t, err)
	if len(snapshot.Repositories) != 1 || snapshot.Repositories[0].ID != "before" {
		t.Fatalf("snapshot repositories=%+v", snapshot.Repositories)
	}
	if _, err := read.Finish(ctx); err == nil {
		t.Fatal("a finished snapshot was read again")
	}
}

// A backup reads the database the store opened. When another state folder
// takes the store's path while it runs, the snapshot refuses instead of
// reading the other database.
func TestPortableReadRefusesAnotherDatabaseAtThePath(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	serving := filepath.Join(parent, "state")
	store, err := Open(ctx, serving)
	noErr(t, err)
	defer store.Close()
	noErr(t, store.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))

	other := filepath.Join(parent, "other")
	replacement, err := Open(ctx, other)
	noErr(t, err)
	noErr(t, replacement.CompleteSetup(ctx, t.TempDir(), "open", "", "admin-hash", true))
	noErr(t, replacement.AddRepository(ctx, Repository{ID: "replacement", Name: "replacement", CreatedAt: time.Unix(1_800_000_000, 0)}))
	noErr(t, replacement.Close())

	if err := os.Rename(serving, filepath.Join(parent, "moved")); err != nil {
		t.Skipf("this system does not let a state folder in use move: %v", err)
	}
	noErr(t, os.Rename(other, serving))
	if _, err := store.BeginPortableRead(ctx); !errors.Is(err, ErrDatabaseReplaced) {
		t.Fatalf("snapshot of a replaced state err=%v", err)
	}
}
