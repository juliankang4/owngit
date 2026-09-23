package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// Tests for the deletion marker: the file in the repository folder that
// shows a later start it inspects the storage the deletion began on.

func crashAt(manager *Manager, at string) {
	manager.deletionHook = func(step string) error {
		if step == at {
			return errors.New("simulated crash")
		}
		return nil
	}
}

func sampleMarkerPath(manager *Manager) string {
	return filepath.Join(manager.RepositoryRoot(), deletionMarkerPrefix+"sample")
}

func recordedDeletion(t *testing.T, manager *Manager) state.RepositoryDeletion {
	t.Helper()
	deletion, exists, err := manager.Store.RepositoryDeletion(context.Background(), "sample")
	if err != nil || !exists {
		t.Fatalf("deletion intent exists=%v err=%v", exists, err)
	}
	return deletion
}

// A crash after the marker is durable but before the commit point leaves a
// marker and no intent. Nothing happens at the next start, and the next
// deletion replaces the marker and removes it.
func TestDeletionMarkerLeftBeforeCommitIsReplaced(t *testing.T) {
	ctx := context.Background()
	manager, remote := newDeletionRepository(t)
	root, err := manager.CanonicalStorageRoot()
	noErr(t, err)
	noErr(t, writeDeletionMarker(root, "sample", testMarkerToken))
	noErr(t, manager.ReconcileDeletions(ctx))
	if _, err := os.Stat(remote); err != nil {
		t.Fatalf("repository touched: %v", err)
	}
	if _, exists, err := manager.Store.Repository(ctx, "sample"); err != nil || !exists {
		t.Fatalf("repository row exists=%v err=%v", exists, err)
	}
	crashAt(manager, "recorded")
	if _, err := manager.Delete(ctx, "sample", DeleteFiles); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("interrupted deletion error=%v", err)
	}
	manager.deletionHook = nil
	if token := recordedDeletion(t, manager).Marker; token == testMarkerToken {
		t.Fatal("the new deletion reused the old token")
	}
	noErr(t, manager.ReconcileDeletions(ctx))
	if _, err := os.Lstat(sampleMarkerPath(manager)); !os.IsNotExist(err) {
		t.Fatalf("marker remains: %v", err)
	}
}

// The intent goes before the marker. A crash between the two leaves only a
// harmless marker; the name is free and a later deletion finishes normally.
func TestDeletionMarkerOutlivesIntentOnlyAsHarmlessLeftover(t *testing.T) {
	ctx := context.Background()
	manager, _ := newDeletionRepository(t)
	crashAt(manager, "finished")
	if _, err := manager.Delete(ctx, "sample", DeleteFiles); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("interrupted deletion error=%v", err)
	}
	manager.deletionHook = nil
	if deletions, err := manager.Store.RepositoryDeletions(ctx); err != nil || len(deletions) != 0 {
		t.Fatalf("intent remains %+v err=%v", deletions, err)
	}
	if _, err := os.Lstat(sampleMarkerPath(manager)); err != nil {
		t.Fatalf("marker was removed before the intent: %v", err)
	}
	noErr(t, manager.ReconcileDeletions(ctx))
	if _, err := manager.Create(ctx, "sample", ""); err != nil {
		t.Fatalf("reuse with a leftover marker: %v", err)
	}
	if _, err := manager.Delete(ctx, "sample", DeleteKeepFiles); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(sampleMarkerPath(manager)); !os.IsNotExist(err) {
		t.Fatalf("marker remains after the second deletion: %v", err)
	}
}

// A marker removed by hand, emptied or holding another deletion's token keeps
// the deletion recorded and its files untouched. A marker recreated with the
// logged token line lets it finish.
func TestDeletionMarkerMustHoldTheDeletionToken(t *testing.T) {
	ctx := context.Background()
	manager, remote := newDeletionRepository(t)
	crashAt(manager, "recorded")
	if _, err := manager.Delete(ctx, "sample", DeleteFiles); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("interrupted deletion error=%v", err)
	}
	manager.deletionHook = nil
	tokenLine := deletionMarkerTokenLine(recordedDeletion(t, manager).Marker)
	noErr(t, os.Remove(sampleMarkerPath(manager)))
	for name, content := range map[string]*string{
		"missing": nil, "empty": new(string), "another token": ptr("token " + testMarkerToken + "\n"),
	} {
		if content != nil {
			noErr(t, os.WriteFile(sampleMarkerPath(manager), []byte(*content), 0o600))
		}
		err := manager.ReconcileDeletions(ctx)
		if !errors.Is(err, ErrDeleteIncomplete) || !strings.Contains(err.Error(), tokenLine) {
			t.Fatalf("%s marker error=%v", name, err)
		}
		if _, err := os.Stat(remote); err != nil {
			t.Fatalf("files touched with a %s marker: %v", name, err)
		}
		if _, err := manager.Create(ctx, "sample", ""); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("name reused with a %s marker: %v", name, err)
		}
	}
	noErr(t, os.WriteFile(sampleMarkerPath(manager), []byte(tokenLine+"\n"), 0o600))
	noErr(t, manager.ReconcileDeletions(ctx))
	if _, err := os.Lstat(remote); !os.IsNotExist(err) {
		t.Fatalf("not deleted after the marker was recreated: %v", err)
	}
}

func ptr(value string) *string { return &value }

// A leftover marker on an older copy of the storage does not stand in for
// the marker of a later deletion of the same ID.
func TestDeletionMarkerOnOlderStorageCopyIsRefused(t *testing.T) {
	ctx := context.Background()
	manager, _ := newDeletionRepository(t)
	crashAt(manager, "finished")
	if _, err := manager.Delete(ctx, "sample", DeleteKeepFiles); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("interrupted deletion error=%v", err)
	}
	manager.deletionHook = nil
	if _, err := manager.Create(ctx, "sample", ""); err != nil {
		t.Fatal(err)
	}
	root := manager.RepositoryRoot()
	older := root + ".older"
	noErr(t, os.Mkdir(older, 0o700))
	copyTree(t, root, older)
	if _, err := os.Lstat(filepath.Join(older, deletionMarkerPrefix+"sample")); err != nil {
		t.Fatalf("the older copy has no leftover marker: %v", err)
	}
	crashAt(manager, "recorded")
	if _, err := manager.Delete(ctx, "sample", DeleteFiles); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("interrupted deletion error=%v", err)
	}
	manager.deletionHook = nil

	current := root + ".current"
	noErr(t, os.Rename(root, current))
	noErr(t, os.Rename(older, root))
	before := treeDigest(t, root)
	if err := manager.ReconcileDeletions(ctx); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("reconciliation on the older copy error=%v", err)
	}
	assertSameTree(t, before, treeDigest(t, root))
	if deletions, err := manager.Store.RepositoryDeletions(ctx); err != nil || len(deletions) != 1 {
		t.Fatalf("intent was dropped %+v err=%v", deletions, err)
	}

	noErr(t, os.Rename(root, older))
	noErr(t, os.Rename(current, root))
	noErr(t, manager.ReconcileDeletions(ctx))
	if _, err := os.Lstat(filepath.Join(root, "sample.git")); !os.IsNotExist(err) {
		t.Fatalf("repository path remains: %v", err)
	}
}

// A copy of the whole storage on a new disk carries the marker with it, so
// the deletion still finishes there.
func TestDeletionFinishesOnCopiedStorage(t *testing.T) {
	ctx := context.Background()
	manager, _ := newDeletionRepository(t)
	crashAt(manager, "moved")
	if _, err := manager.Delete(ctx, "sample", DeleteKeepFiles); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("interrupted deletion error=%v", err)
	}
	manager.deletionHook = nil
	root := manager.RepositoryRoot()
	noErr(t, os.Rename(root, root+".old"))
	noErr(t, os.Mkdir(root, 0o700))
	copyTree(t, root+".old", root)
	noErr(t, manager.ReconcileDeletions(ctx))
	if entries, err := os.ReadDir(filepath.Join(root, removedDirectoryName)); err != nil || len(entries) != 1 {
		t.Fatalf("kept entries on the copy %v err=%v", entries, err)
	}
	if _, err := os.Lstat(sampleMarkerPath(manager)); !os.IsNotExist(err) {
		t.Fatalf("marker remains: %v", err)
	}
}

// A link or directory in the marker's place is never followed or accepted.
func TestDeletionMarkerMustBeRegularFile(t *testing.T) {
	ctx := context.Background()
	outside := t.TempDir()
	target := filepath.Join(outside, "file")
	noErr(t, os.WriteFile(target, []byte("x"), 0o600))
	for _, kind := range []string{"link", "directory"} {
		t.Run(kind, func(t *testing.T) {
			manager, remote := newDeletionRepository(t)
			crashAt(manager, "recorded")
			if _, err := manager.Delete(ctx, "sample", DeleteFiles); !errors.Is(err, ErrDeleteIncomplete) {
				t.Fatalf("interrupted deletion error=%v", err)
			}
			manager.deletionHook = nil
			// The link target holds the right token, so only the file type refuses it.
			noErr(t, os.WriteFile(target, []byte(deletionMarkerTokenLine(recordedDeletion(t, manager).Marker)+"\n"), 0o600))
			noErr(t, os.Remove(sampleMarkerPath(manager)))
			if kind == "link" {
				if err := os.Symlink(target, sampleMarkerPath(manager)); err != nil {
					t.Skip(err)
				}
			} else {
				noErr(t, os.Mkdir(sampleMarkerPath(manager), 0o700))
			}
			if err := manager.ReconcileDeletions(ctx); !errors.Is(err, ErrDeleteIncomplete) || !strings.Contains(err.Error(), "is not a regular file") {
				t.Fatalf("%s marker error=%v", kind, err)
			}
			if _, err := os.Stat(remote); err != nil {
				t.Fatalf("files touched: %v", err)
			}
		})
	}
	t.Run("new deletion refuses a linked marker", func(t *testing.T) {
		noErr(t, os.WriteFile(target, []byte("x"), 0o600))
		manager, remote := newDeletionRepository(t)
		if err := os.Symlink(target, sampleMarkerPath(manager)); err != nil {
			t.Skip(err)
		}
		if _, err := manager.Delete(ctx, "sample", DeleteFiles); err == nil {
			t.Fatal("deletion with a linked marker was accepted")
		}
		if _, exists, err := manager.Store.Repository(ctx, "sample"); err != nil || !exists {
			t.Fatalf("repository row exists=%v err=%v", exists, err)
		}
		if _, err := os.Stat(remote); err != nil {
			t.Fatal(err)
		}
		if content, _ := os.ReadFile(target); string(content) != "x" {
			t.Fatal("link target changed")
		}
	})
}

// A deletion refused inside its commit transaction removes the marker it
// wrote.
func TestDeletionMarkerRemovedWhenCommitIsRefused(t *testing.T) {
	ctx := context.Background()
	manager, remote := newDeletionRepository(t)
	lock := manager.Locks.For("sample")
	lock.RLock()
	done := make(chan error, 1)
	go func() {
		_, err := manager.Delete(ctx, "sample", DeleteFiles)
		done <- err
	}()
	// Delete passes its first busy check and waits for the lock.
	time.Sleep(500 * time.Millisecond)
	now := time.Unix(1_800_000_000, 0)
	source, err := manager.Store.ConfigureImportSource(ctx, state.ImportSourceInput{
		RepositoryID: "sample", URL: "https://example.invalid/team/sample.git", Mode: state.ImportModeStandalone, Now: now,
	})
	noErr(t, err)
	noErr(t, manager.Store.BeginImportRun(ctx, state.ImportRun{
		ID: strings.Repeat("a", 32), RepositoryID: "sample", SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision,
		Kind: state.ImportKindRefresh, Status: state.ImportRunFetching, StartedAt: now, CreatedAt: now,
	}))
	lock.RUnlock()
	if err := <-done; !errors.Is(err, ErrImportRunning) {
		t.Fatalf("refused deletion error=%v", err)
	}
	if _, err := os.Lstat(sampleMarkerPath(manager)); !os.IsNotExist(err) {
		t.Fatalf("marker left after a refused commit: %v", err)
	}
	if _, err := os.Stat(remote); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	noErr(t, filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(to, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm()|0o700)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, content, info.Mode().Perm()|0o600)
	}))
}
