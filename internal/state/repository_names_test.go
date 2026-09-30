package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

func namesTestStore(t *testing.T, ids ...string) *Store {
	t.Helper()
	store := openTestStore(t)
	noErr(t, store.CompleteSetup(context.Background(), t.TempDir(), "open", "", "synthetic-admin-hash", true))
	for _, id := range ids {
		noErr(t, store.AddRepository(context.Background(), Repository{ID: id, Name: id, CreatedAt: time.Unix(1_800_000_000, 0)}))
	}
	return store
}

func wantAddress(t *testing.T, store *Store, name string, now time.Time, id, current string) {
	t.Helper()
	address, found, err := store.ResolveRepositoryName(context.Background(), name, now)
	noErr(t, err)
	if id == "" {
		if found {
			t.Fatalf("%q reached %+v, want nothing", name, address)
		}
		return
	}
	if !found || address.RepositoryID != id || address.Current != current {
		t.Fatalf("%q reached %+v (found %v), want %s at %s", name, address, found, id, current)
	}
}

// A repository answers at its ID until it is renamed, then at its new name;
// the old address leads there until its alias expires, and renaming back to
// the ID makes the ID the address again.
func TestRenameMovesTheAddressAndKeepsAnAlias(t *testing.T) {
	ctx := context.Background()
	store := namesTestStore(t, "project")
	now := time.Unix(1_800_000_000, 0)
	wantAddress(t, store, "project", now, "project", "project")

	renamed, err := store.RenameRepository(ctx, "project", "Renamed", now)
	noErr(t, err)
	if renamed.ID != "project" || renamed.Name != "Renamed" || renamed.Address != "renamed" {
		t.Fatalf("renamed=%+v", renamed)
	}
	stored, _, err := store.Repository(ctx, "project")
	noErr(t, err)
	if stored.Name != "Renamed" || stored.Address != "renamed" {
		t.Fatalf("stored=%+v", stored)
	}
	wantAddress(t, store, "renamed", now, "project", "renamed")
	wantAddress(t, store, "project", now, "project", "renamed")
	end := now.Add(RepositoryAliasLifetime)
	wantAddress(t, store, "project", end.Add(-time.Second), "project", "renamed")
	wantAddress(t, store, "project", end, "", "")
	aliases, err := store.RepositoryAliases(ctx, "project", now)
	noErr(t, err)
	if len(aliases) != 1 || aliases[0].Name != "project" || !aliases[0].AliasUntil.Equal(end) {
		t.Fatalf("aliases=%+v", aliases)
	}

	// A second rename keeps both earlier addresses leading to the newest.
	later := now.Add(time.Hour)
	_, err = store.RenameRepository(ctx, "project", "third", later)
	noErr(t, err)
	wantAddress(t, store, "renamed", later, "project", "third")
	wantAddress(t, store, "project", later, "project", "third")

	// Renaming back to the ID removes the current name; the ID's alias row
	// is not kept, and the last name becomes an alias.
	_, err = store.RenameRepository(ctx, "project", "PROJECT", later)
	noErr(t, err)
	wantAddress(t, store, "project", later.Add(2*RepositoryAliasLifetime), "project", "project")
	wantAddress(t, store, "third", later, "project", "project")
	snapshot, err := store.RecoverySnapshot(ctx)
	noErr(t, err)
	for _, name := range snapshot.RepositoryNames {
		if name.Kind == RepositoryNameCurrent || name.Name == "project" {
			t.Fatalf("names after renaming back=%+v", snapshot.RepositoryNames)
		}
	}
	noErr(t, ValidateRepositoryRecords(snapshot))
}

// A name that is another repository's ID, current name or unexpired alias
// is refused in any letter case, and the refused rename changes nothing. An
// expired alias of another repository is free and removed when taken.
func TestRenameRefusesNamesOfOtherRepositories(t *testing.T) {
	ctx := context.Background()
	store := namesTestStore(t, "first", "second")
	now := time.Unix(1_800_000_000, 0)
	_, err := store.RenameRepository(ctx, "second", "moved", now)
	noErr(t, err)
	for _, name := range []string{"SECOND", "Moved"} {
		if _, err := store.RenameRepository(ctx, "first", name, now); !errors.Is(err, ErrRepositoryNameTaken) {
			t.Fatalf("rename to %q err=%v", name, err)
		}
		wantAddress(t, store, "first", now, "first", "first")
	}
	if taken, err := store.RepositoryNameInUse(ctx, "moved", now); err != nil || !taken {
		t.Fatalf("moved in use=%v err=%v", taken, err)
	}
	// The ID of the renamed repository stays its own even after its alias
	// expires.
	expired := now.Add(RepositoryAliasLifetime)
	if _, err := store.RenameRepository(ctx, "first", "second", expired); !errors.Is(err, ErrRepositoryNameTaken) {
		t.Fatalf("rename to an ID with an expired alias err=%v", err)
	}
	if taken, err := store.RepositoryNameInUse(ctx, "second", expired); err != nil || !taken {
		t.Fatalf("second in use=%v err=%v", taken, err)
	}

	// An expired alias that is not an ID is free again.
	_, err = store.RenameRepository(ctx, "second", "again", now)
	noErr(t, err)
	if _, err := store.RenameRepository(ctx, "first", "moved", now); !errors.Is(err, ErrRepositoryNameTaken) {
		t.Fatalf("rename to a live alias err=%v", err)
	}
	if taken, err := store.RepositoryNameInUse(ctx, "moved", expired); err != nil || taken {
		t.Fatalf("expired alias in use=%v err=%v", taken, err)
	}
	_, err = store.RenameRepository(ctx, "first", "moved", expired)
	noErr(t, err)
	wantAddress(t, store, "moved", expired, "first", "moved")
}

// A new repository cannot take a live name as its ID, and takes an expired
// alias, which is removed in the same transaction.
func TestAddRepositoryChecksNames(t *testing.T) {
	ctx := context.Background()
	store := namesTestStore(t, "project")
	now := time.Now()
	_, err := store.RenameRepository(ctx, "project", "renamed", now)
	noErr(t, err)
	for _, id := range []string{"renamed", "project"} {
		if err := store.AddRepository(ctx, Repository{ID: id, Name: id, CreatedAt: now}); !errors.Is(err, ErrRepositoryNameTaken) {
			t.Fatalf("add %q err=%v", id, err)
		}
	}
	_, err = store.RenameRepository(ctx, "project", "latest", now)
	noErr(t, err)
	noErr(t, store.Exec(ctx, `UPDATE repository_names SET alias_until=? WHERE name='renamed'`, now.Add(-time.Second).Unix()))
	noErr(t, store.AddRepository(ctx, Repository{ID: "renamed", Name: "renamed", CreatedAt: now}))
	wantAddress(t, store, "renamed", now, "renamed", "renamed")
}

// A rename is refused while an import runs for the repository, with nothing
// changed; deleting the repository removes its names.
func TestRenameRefusesABusyRepositoryAndDeletionRemovesNames(t *testing.T) {
	ctx := context.Background()
	store := namesTestStore(t, "project")
	now := time.Unix(1_800_000_000, 0)
	_, err := store.RenameRepository(ctx, "project", "renamed", now)
	noErr(t, err)
	_, err = store.ConfigureImportSource(ctx, ImportSourceInput{RepositoryID: "project", URL: "https://example.invalid/source.git", Mode: ImportModeStandalone, Now: now})
	noErr(t, err)
	noErr(t, store.Exec(ctx, `INSERT INTO import_runs(id,repository_id,kind,status,source_generation,authority_revision,started_at,created_at)
		VALUES('00000000000000000000000000000001','project','refresh','fetching',1,1,?1,?1)`, now.Unix()))
	if _, err := store.RenameRepository(ctx, "project", "other", now); !errors.Is(err, ErrRepositoryDeletionImportActive) {
		t.Fatalf("rename during an import err=%v", err)
	}
	wantAddress(t, store, "renamed", now, "project", "renamed")
	noErr(t, store.Exec(ctx, `DELETE FROM import_runs`))

	noErr(t, store.BeginRepositoryDeletion(ctx, RepositoryDeletion{RepositoryID: "project", Mode: RepositoryDeletionDeleteFiles, Root: t.TempDir(), Moved: ".owngit-removed/x", Marker: "00000000000000000000000000000002", CreatedAt: now}))
	if count, err := store.TableRowCount(ctx, "repository_names"); err != nil || count != 0 {
		t.Fatalf("names after deletion=%d err=%v", count, err)
	}
}
