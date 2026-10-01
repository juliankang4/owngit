package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/publishdir"
	"owngit/internal/state"
)

// The page limit produces real SQLITE_FULL at AddRepository, after Git has
// initialized and published the empty folder on a separate writable volume.
func TestCreateWithFullStateLeavesNameUsableAfterRecoveryAndRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "same-process"
		if restart {
			name = "restart-before-retry"
		}
		t.Run(name, func(t *testing.T) {
			manager, _, _ := newTestRepository(t)
			ctx := context.Background()
			noErr(t, manager.Store.Exec(ctx, `CREATE TRIGGER fill_creation BEFORE INSERT ON repositories BEGIN INSERT INTO metadata(key,value) VALUES ('creation-space',hex(zeroblob(1048576))); END`))
			noErr(t, manager.Store.Exec(ctx, `PRAGMA max_page_count=1`))
			_, err := manager.Create(ctx, "fresh", "")
			if err == nil || !strings.Contains(err.Error(), "database or disk is full") {
				t.Fatalf("create error=%v, want SQLITE_FULL", err)
			}
			path := filepath.Join(manager.RepositoryRoot(), "fresh.git")
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("unaccepted folder remains: %v", err)
			}
			if _, found, err := manager.Store.Repository(ctx, "fresh"); err != nil || found {
				t.Fatalf("failed creation found=%v err=%v", found, err)
			}
			noErr(t, manager.Store.Exec(ctx, `PRAGMA max_page_count=1073741823`))
			noErr(t, manager.Store.Exec(ctx, `DROP TRIGGER fill_creation`))
			if restart {
				stateDir := manager.Store.Dir()
				noErr(t, manager.Store.Close())
				store, err := state.Open(ctx, stateDir)
				noErr(t, err)
				t.Cleanup(func() { _ = store.Close() })
				manager.Store = store
			}
			_, err = manager.Create(ctx, "fresh", "recovered")
			noErr(t, err)
			if row, found, err := manager.Store.Repository(ctx, "fresh"); err != nil || !found || row.Description != "recovered" {
				t.Fatalf("recovered row=%+v found=%v err=%v", row, found, err)
			}
		})
	}
}

// This is the released creation order: initialize, rename, then fail recording.
// A restart cannot prove that it owns such an older unrecorded folder.
func TestCreatePreservesAnOlderUnrecordedFolder(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	ctx := context.Background()
	staging := filepath.Join(manager.RepositoryRoot(), ".older-create")
	noErr(t, os.Mkdir(staging, 0o700))
	noErr(t, manager.InitBareRepository(ctx, staging, CreateOptions{}))
	path := filepath.Join(manager.RepositoryRoot(), "older.git")
	noErr(t, publishdir.Rename(ctx, staging, path))
	noErr(t, manager.Store.Exec(ctx, `CREATE TRIGGER refuse_creation BEFORE INSERT ON repositories BEGIN SELECT RAISE(ABORT,'recording refused'); END`))
	if err := manager.Store.AddRepository(ctx, state.Repository{ID: "older", Name: "older"}); err == nil {
		t.Fatal("old recording failure was not exercised")
	}
	noErr(t, manager.Store.Exec(ctx, `DROP TRIGGER refuse_creation`))
	stateDir := manager.Store.Dir()
	noErr(t, manager.Store.Close())
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	manager.Store = store
	before, err := os.ReadFile(filepath.Join(path, "HEAD"))
	noErr(t, err)
	_, err = manager.Create(ctx, "older", "")
	if !errors.Is(err, ErrFolderExists) || !strings.Contains(err.Error(), "choose another name") {
		t.Fatalf("older folder error=%v", err)
	}
	after, err := os.ReadFile(filepath.Join(path, "HEAD"))
	noErr(t, err)
	if string(after) != string(before) {
		t.Fatal("the older folder was changed")
	}
	_, err = manager.Create(ctx, "another-name", "")
	noErr(t, err)
}

func TestCreationRollbackPreservesMovedRootLink(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	path := filepath.Join(manager.RepositoryRoot(), "draft")
	noErr(t, os.Mkdir(path, 0o700))
	noErr(t, manager.InitBareRepository(context.Background(), path, CreateOptions{}))
	creation, err := captureEmptyCreation(path)
	noErr(t, err)
	t.Cleanup(func() { _ = creation.parent.Close() })
	moved := path + "-moved"
	noErr(t, os.Rename(path, moved))
	if err := os.Symlink(moved, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := creation.rollback(path); err == nil {
		t.Error("rollback accepted a link to the moved original")
	}
	if _, err := os.Stat(filepath.Join(moved, "config")); err != nil {
		t.Fatalf("rollback changed the moved tree: %v", err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("rollback changed the substituted link: %v %v", info, err)
	}
}

func TestCreationSnapshotDoesNotBlockPublication(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	path := filepath.Join(manager.RepositoryRoot(), "draft")
	noErr(t, os.Mkdir(path, 0o700))
	noErr(t, manager.InitBareRepository(context.Background(), path, CreateOptions{}))
	creation, err := captureEmptyCreation(path)
	noErr(t, err)
	t.Cleanup(func() { _ = creation.parent.Close() })
	published := filepath.Join(manager.RepositoryRoot(), "published.git")
	noErr(t, publishdir.Rename(context.Background(), path, published))
	noErr(t, creation.rollback(published))
	if _, err := os.Lstat(published); !os.IsNotExist(err) {
		t.Fatalf("unaccepted publication remains: %v", err)
	}
	if !strings.HasPrefix(creation.preserved, filepath.Join(manager.RepositoryRoot(), failedCreateDirectory)+string(filepath.Separator)) {
		t.Fatalf("unexpected preservation path: %s", creation.preserved)
	}
	if _, err := os.Stat(filepath.Join(creation.preserved, "config")); err != nil {
		t.Fatalf("rollback deleted initialized files: %v", err)
	}
	// Later replacements and added data are never subject to a cleanup pass.
	noErr(t, os.Rename(filepath.Join(creation.preserved, "config"), filepath.Join(creation.preserved, "original-config")))
	noErr(t, os.WriteFile(filepath.Join(creation.preserved, "config"), []byte("new data"), 0o600))
	noErr(t, os.WriteFile(filepath.Join(creation.preserved, "objects", "keep"), []byte("later history"), 0o600))
	if err := creation.unchanged(filepath.Join(failedCreateDirectory, filepath.Base(creation.preserved))); err == nil {
		t.Fatal("changed preserved content was considered the original empty tree")
	}
	if data, err := os.ReadFile(filepath.Join(creation.preserved, "config")); err != nil || string(data) != "new data" {
		t.Fatalf("replacement data was deleted: %q %v", data, err)
	}
}

func TestFailedRollbackMoveKeepsPublishedTree(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix write-permission enforcement for this account")
	}
	manager, _, _ := newTestRepository(t)
	path := filepath.Join(manager.RepositoryRoot(), "draft")
	noErr(t, os.Mkdir(path, 0o700))
	noErr(t, manager.InitBareRepository(context.Background(), path, CreateOptions{}))
	creation, err := captureEmptyCreation(path)
	noErr(t, err)
	t.Cleanup(func() { _ = creation.parent.Close() })
	keptRoot := filepath.Join(manager.RepositoryRoot(), failedCreateDirectory)
	noErr(t, os.Mkdir(keptRoot, 0o500))
	t.Cleanup(func() { _ = os.Chmod(keptRoot, 0o700) })
	if err := creation.rollback(path); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("move failure=%v, want permission refusal", err)
	}
	if creation.preserved != "" {
		t.Fatal("failed move reported a preserved destination")
	}
	if _, err := os.Stat(filepath.Join(path, "config")); err != nil {
		t.Fatalf("failed move touched the published tree: %v", err)
	}
	if entries, err := os.ReadDir(keptRoot); err != nil || len(entries) != 0 {
		t.Fatalf("failed move left partial entries: %v %v", entries, err)
	}
}

func TestCreationRollbackPreservesReplacementsAndAddedData(t *testing.T) {
	for _, change := range []string{"replacement", "extra-file", "changed-file", "symlink"} {
		t.Run(change, func(t *testing.T) {
			manager, _, _ := newTestRepository(t)
			path := filepath.Join(manager.RepositoryRoot(), "draft")
			noErr(t, os.Mkdir(path, 0o700))
			noErr(t, manager.InitBareRepository(context.Background(), path, CreateOptions{}))
			creation, err := captureEmptyCreation(path)
			noErr(t, err)
			t.Cleanup(func() { _ = creation.parent.Close() })
			switch change {
			case "replacement":
				noErr(t, os.Rename(path, path+"-original"))
				noErr(t, os.Mkdir(path, 0o700))
				noErr(t, os.WriteFile(filepath.Join(path, "keep"), []byte("unrelated data"), 0o600))
			case "extra-file":
				noErr(t, os.WriteFile(filepath.Join(path, "objects", "keep"), []byte("new history"), 0o600))
			case "changed-file":
				noErr(t, os.WriteFile(filepath.Join(path, "HEAD"), []byte("ref: refs/heads/new-history\n"), 0o600))
			case "symlink":
				if err := os.Symlink(path+"-outside", filepath.Join(path, "unexpected-link")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if err := creation.rollback(path); err == nil {
				t.Fatal("rollback accepted changed or replaced storage")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("changed storage removed: %v", err)
			}
			if change == "replacement" {
				data, err := os.ReadFile(filepath.Join(path, "keep"))
				if err != nil || string(data) != "unrelated data" {
					t.Fatalf("replacement changed: %q %v", data, err)
				}
			} else if _, err := os.Stat(filepath.Join(path, "config")); err != nil {
				t.Fatalf("rollback modified the changed tree: %v", err)
			}
		})
	}
}
