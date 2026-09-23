package repository

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/state"
)

var testDeletionTime = time.Date(2027, 1, 15, 8, 0, 0, 0, time.UTC)

// testMarkerToken is the marker token of deletion intents that tests record
// directly.
var testMarkerToken = strings.Repeat("c", 32)

// newDeletionRepository returns a repository with a branch, a tag and
// retained history from a real force-push, and a fixed kept-folder time.
func newDeletionRepository(t *testing.T) (*Manager, string) {
	t.Helper()
	manager, remote, work := newTestRepository(t)
	manager.deletionClock = func() time.Time { return testDeletionTime }
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "tag", "v1")
	commitFile(t, work, "two", "two", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "refs/tags/v1")
	replaced := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "--force", "origin", "HEAD~1:refs/heads/main")
	assertRef(t, remote, "refs/owngit/retained/heads/"+replaced, replaced)
	return manager, remote
}

// treeDigest records every entry below directory with its mode and content,
// without following links.
func treeDigest(t *testing.T, directory string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	noErr(t, filepath.WalkDir(directory, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, current)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			value += ":" + string(content)
		}
		entries[filepath.ToSlash(relative)] = value
		return nil
	}))
	return entries
}

func assertSameTree(t *testing.T, want, got map[string]string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("tree has %d entries, want %d", len(got), len(want))
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("entry %s changed", name)
		}
	}
}

func assertRepositoryGone(t *testing.T, manager *Manager, id string) {
	t.Helper()
	ctx := context.Background()
	if _, exists, err := manager.Store.Repository(ctx, id); err != nil || exists {
		t.Fatalf("repository row exists=%v err=%v", exists, err)
	}
	if deletions, err := manager.Store.RepositoryDeletions(ctx); err != nil || len(deletions) != 0 {
		t.Fatalf("deletion intents remain %+v err=%v", deletions, err)
	}
	listed, err := manager.Store.Repositories(ctx)
	noErr(t, err)
	for _, repository := range listed {
		if repository.ID == id || strings.HasPrefix(repository.ID, ".") {
			t.Fatalf("listing includes %q", repository.ID)
		}
	}
	path, err := manager.Path(id)
	noErr(t, err)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("repository path remains: %v", err)
	}
}

// rootEntries lists the names directly inside the repository root.
func rootEntries(t *testing.T, manager *Manager) []string {
	t.Helper()
	entries, err := os.ReadDir(manager.RepositoryRoot())
	noErr(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestDeleteKeepFilesMovesRepositoryUnchangedAndFreesName(t *testing.T) {
	ctx := context.Background()
	manager, remote := newDeletionRepository(t)
	before := treeDigest(t, remote)

	result, err := manager.Delete(ctx, "sample", DeleteKeepFiles)
	noErr(t, err)
	want := filepath.Join(manager.RepositoryRoot(), ".owngit-removed", "sample-20270115T080000Z.git")
	if result.KeptPath != want {
		t.Fatalf("kept path=%q want %q", result.KeptPath, want)
	}
	assertRepositoryGone(t, manager, "sample")
	assertSameTree(t, before, treeDigest(t, result.KeptPath))
	if _, err := manager.Create(ctx, "sample", "reused name"); err != nil {
		t.Fatalf("name was not reusable: %v", err)
	}
	if listed, err := manager.Store.Repositories(ctx); err != nil || len(listed) != 1 || listed[0].ID != "sample" {
		t.Fatalf("listing after reuse=%+v err=%v", listed, err)
	}
	// A second deletion in the same second gets a numbered folder.
	second, err := manager.Delete(ctx, "sample", DeleteKeepFiles)
	noErr(t, err)
	if second.KeptPath != filepath.Join(manager.RepositoryRoot(), ".owngit-removed", "sample-20270115T080000Z-2.git") {
		t.Fatalf("collision path=%q", second.KeptPath)
	}
	assertSameTree(t, before, treeDigest(t, result.KeptPath))
	if _, err := manager.Delete(ctx, "sample", DeleteKeepFiles); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("deleting a missing repository error=%v", err)
	}
}

// The documented way back: create a new repository and push the kept
// folder's branches and tags into it. The command succeeds although the kept
// folder holds retained history, which is not transferred.
func TestKeptRepositoryCanBePushedIntoNewRepository(t *testing.T) {
	ctx := context.Background()
	manager, remote := newDeletionRepository(t)
	mainOID := gitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main")
	tagOID := gitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/tags/v1")
	result, err := manager.Delete(ctx, "sample", DeleteKeepFiles)
	noErr(t, err)
	if kept := gitOutput(t, "", "--git-dir", result.KeptPath, "for-each-ref", "refs/owngit/retained/"); kept == "" {
		t.Fatal("kept folder has no retained history to exercise")
	}
	if _, err := manager.Create(ctx, "returned", ""); err != nil {
		t.Fatal(err)
	}
	returned, err := manager.Path("returned")
	noErr(t, err)
	runGit(t, "", "--git-dir", result.KeptPath, "push", returned, "refs/heads/*:refs/heads/*", "refs/tags/*:refs/tags/*")
	assertRef(t, returned, "refs/tags/v1", tagOID)
	assertRef(t, returned, "refs/heads/main", mainOID)
	if hidden := gitOutput(t, "", "--git-dir", returned, "for-each-ref", "refs/owngit/"); hidden != "" {
		t.Fatalf("retained refs were copied: %s", hidden)
	}
}

func TestDeleteFilesRemovesDirectoryAndRetainedHistory(t *testing.T) {
	ctx := context.Background()
	manager, _ := newDeletionRepository(t)
	result, err := manager.Delete(ctx, "sample", DeleteFiles)
	noErr(t, err)
	if result.KeptPath != "" {
		t.Fatalf("delete mode reported a kept path %q", result.KeptPath)
	}
	assertRepositoryGone(t, manager, "sample")
	if names := rootEntries(t, manager); len(names) != 0 {
		t.Fatalf("repository root keeps %v", names)
	}
	if _, err := manager.Create(ctx, "sample", ""); err != nil {
		t.Fatalf("name was not reusable: %v", err)
	}
	remote, err := manager.Path("sample")
	noErr(t, err)
	if output, err := gitCombined("", "--git-dir", remote, "for-each-ref"); err != nil || strings.TrimSpace(output) != "" {
		t.Fatalf("reused name inherited refs %q err=%v", output, err)
	}
}

func TestDeleteRefusesBusyRepository(t *testing.T) {
	ctx := context.Background()
	manager, remote := newDeletionRepository(t)
	store := manager.Store
	now := time.Unix(1_800_000_000, 0)

	source, err := store.ConfigureImportSource(ctx, state.ImportSourceInput{
		RepositoryID: "sample", URL: "https://example.invalid/team/sample.git", Mode: state.ImportModeStandalone, Now: now,
	})
	noErr(t, err)
	run := state.ImportRun{
		ID: strings.Repeat("a", 32), RepositoryID: "sample", SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision,
		Kind: state.ImportKindRefresh, Status: state.ImportRunFetching, StartedAt: now, CreatedAt: now,
	}
	noErr(t, store.BeginImportRun(ctx, run))
	if _, err := manager.Delete(ctx, "sample", DeleteFiles); !errors.Is(err, ErrImportRunning) || !errors.Is(err, ErrRepositoryBusy) {
		t.Fatalf("active import error=%v", err)
	}
	run.Status, run.FinishedAt = state.ImportRunFailed, now
	noErr(t, store.FinishImportRun(ctx, run))

	if _, err := store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "sample", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"},
		MaxTimeoutMS: 60000, MaxOutputLimitBytes: 65536, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantCheckConsent(ctx, "sample", now); err != nil {
		t.Fatal(err)
	}
	runner, _, _, err := store.IssueCheckRunnerToken(ctx, "sample", "runner", "", now)
	noErr(t, err)
	job := state.CheckJobRequest{
		RepositoryID: "sample", Trigger: "push", EventKey: "refs/heads/main@" + strings.Repeat("b", 40),
		SourceOID: strings.Repeat("b", 40), TriggerRef: "main", WorkflowDigest: strings.Repeat("c", 64),
		Checks: []state.CheckDefinition{{Name: "unit", Command: "true"}},
	}
	if _, _, err := store.AdmitCheckJob(ctx, job, now); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimCheckJob(ctx, "sample", runner.ID, now); err != nil || !claimed {
		t.Fatalf("claim claimed=%v err=%v", claimed, err)
	}
	claimed, err := manager.Delete(ctx, "sample", DeleteKeepFiles)
	if !errors.Is(err, ErrCheckRunning) || !errors.Is(err, ErrRepositoryBusy) {
		t.Fatalf("claimed check error=%v result=%+v", err, claimed)
	}
	// The job ended, but its container cleanup is still unconfirmed.
	jobs, err := store.CheckJobs(ctx, "sample")
	noErr(t, err)
	noErr(t, store.Exec(ctx, `UPDATE check_jobs SET status='interrupted' WHERE id=?`, jobs[0].ID))
	noErr(t, store.Exec(ctx, `INSERT INTO check_job_runtime_ownership(job_id,repository_id,container_name,container_id,daemon_id,created_at) VALUES(?,?,?,?,?,1)`,
		jobs[0].ID, "sample", "owngit-check-test", strings.Repeat("d", 64), "daemon-one"))
	if _, err := manager.Delete(ctx, "sample", DeleteKeepFiles); !errors.Is(err, ErrCheckCleanupPending) || !errors.Is(err, ErrRepositoryBusy) {
		t.Fatalf("pending container cleanup error=%v", err)
	}

	if _, err := manager.Delete(ctx, "other-name", DeleteKeepFiles); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("unknown repository error=%v", err)
	}
	if _, exists, err := store.Repository(ctx, "sample"); err != nil || !exists {
		t.Fatalf("refused deletion removed the repository exists=%v err=%v", exists, err)
	}
	if _, err := os.Stat(remote); err != nil {
		t.Fatalf("refused deletion moved files: %v", err)
	}
}

// A clone, push, restore or merge holds the repository lock.
func TestDeleteReportsInUseAfterBoundedLockWait(t *testing.T) {
	manager, remote := newDeletionRepository(t)
	lock := manager.Locks.For("sample")
	lock.RLock()
	defer lock.RUnlock()
	started := time.Now()
	if _, err := manager.Delete(context.Background(), "sample", DeleteFiles); !errors.Is(err, ErrRepositoryInUse) || !errors.Is(err, ErrRepositoryBusy) {
		t.Fatalf("held lock error=%v", err)
	}
	if elapsed := time.Since(started); elapsed < deleteLockWait || elapsed > deleteLockWait+3*time.Second {
		t.Fatalf("lock wait took %s", elapsed)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Delete(cancelled, "sample", DeleteFiles); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait error=%v", err)
	}
	if _, exists, err := manager.Store.Repository(context.Background(), "sample"); err != nil || !exists {
		t.Fatalf("busy deletion removed the record exists=%v err=%v", exists, err)
	}
	if _, err := os.Stat(remote); err != nil {
		t.Fatalf("busy deletion moved files: %v", err)
	}
}

// Each case stops a deletion right after one durable step, as a crash would,
// and then runs startup reconciliation.
func TestDeleteCompletesAfterInterruptionAtEveryStep(t *testing.T) {
	for _, test := range []struct {
		mode DeleteMode
		step string
	}{
		{DeleteKeepFiles, "recorded"}, {DeleteKeepFiles, "moved"},
		{DeleteFiles, "recorded"}, {DeleteFiles, "moved"}, {DeleteFiles, "marked"}, {DeleteFiles, "removed"},
	} {
		t.Run(string(test.mode)+"/"+test.step, func(t *testing.T) {
			ctx := context.Background()
			manager, remote := newDeletionRepository(t)
			before := treeDigest(t, remote)
			crash := errors.New("simulated crash")
			manager.deletionHook = func(step string) error {
				if step == test.step {
					return crash
				}
				return nil
			}
			if _, err := manager.Delete(ctx, "sample", test.mode); !errors.Is(err, ErrDeleteIncomplete) {
				t.Fatalf("interrupted deletion error=%v", err)
			}
			if _, exists, err := manager.Store.Repository(ctx, "sample"); err != nil || exists {
				t.Fatalf("records remain after the commit point exists=%v err=%v", exists, err)
			}
			if _, err := manager.Create(ctx, "sample", ""); !errors.Is(err, ErrNameTaken) {
				t.Fatalf("name reused while its deletion is unfinished: %v", err)
			}

			manager.deletionHook = nil
			noErr(t, manager.ReconcileDeletions(ctx))
			assertRepositoryGone(t, manager, "sample")
			kept := filepath.Join(manager.RepositoryRoot(), ".owngit-removed", "sample-20270115T080000Z.git")
			if test.mode == DeleteKeepFiles {
				assertSameTree(t, before, treeDigest(t, kept))
			} else if names := rootEntries(t, manager); len(names) != 0 {
				t.Fatalf("repository root keeps %v", names)
			}
			noErr(t, manager.ReconcileDeletions(ctx))
			if _, err := manager.Create(ctx, "sample", ""); err != nil {
				t.Fatalf("name was not reusable after reconciliation: %v", err)
			}
		})
	}
}

func TestDeleteRetryResumesInterruptedDeletion(t *testing.T) {
	ctx := context.Background()
	manager, _ := newDeletionRepository(t)
	manager.deletionHook = func(step string) error {
		if step == "marked" {
			return errors.New("simulated failure")
		}
		return nil
	}
	if _, err := manager.Delete(ctx, "sample", DeleteFiles); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("first attempt error=%v", err)
	}
	manager.deletionHook = nil
	// The unfinished deletion keeps its original mode.
	result, err := manager.Delete(ctx, "sample", DeleteKeepFiles)
	noErr(t, err)
	if result.KeptPath != "" {
		t.Fatalf("resumed delete_files deletion kept %q", result.KeptPath)
	}
	assertRepositoryGone(t, manager, "sample")
	if names := rootEntries(t, manager); len(names) != 0 {
		t.Fatalf("repository root keeps %v", names)
	}
}

// After the directory was renamed, a new repository may take the name. Resuming
// the old deletion must never touch it.
func TestResumedDeletionLeavesNewRepositoryWithSameName(t *testing.T) {
	for _, mode := range []DeleteMode{DeleteKeepFiles, DeleteFiles} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			manager, _ := newDeletionRepository(t)
			manager.deletionHook = func(step string) error {
				if step == "moved" {
					return errors.New("simulated crash")
				}
				return nil
			}
			if _, err := manager.Delete(ctx, "sample", mode); !errors.Is(err, ErrDeleteIncomplete) {
				t.Fatalf("interrupted deletion error=%v", err)
			}
			manager.deletionHook = nil
			if _, err := manager.Create(ctx, "sample", ""); !errors.Is(err, ErrNameTaken) {
				t.Fatalf("name reused during an unfinished deletion: %v", err)
			}
			if err := manager.Store.AddRepository(ctx, state.Repository{ID: "sample", Name: "sample", CreatedAt: time.Now()}); !errors.Is(err, state.ErrRepositoryDeletionPending) {
				t.Fatalf("row recorded during an unfinished deletion: %v", err)
			}
			path := recordRepositoryLikeOlderBuild(t, manager, "sample")
			newTree := treeDigest(t, path)

			noErr(t, manager.ReconcileDeletions(ctx))
			assertSameTree(t, newTree, treeDigest(t, path))
			if _, exists, err := manager.Store.Repository(ctx, "sample"); err != nil || !exists {
				t.Fatalf("new repository row exists=%v err=%v", exists, err)
			}
			if deletions, err := manager.Store.RepositoryDeletions(ctx); err != nil || len(deletions) != 0 {
				t.Fatalf("intent remains %+v err=%v", deletions, err)
			}
		})
	}
}

// recordRepositoryLikeOlderBuild creates a bare repository and its row the way
// an older build that ignores deletion intents would, and returns its path.
func recordRepositoryLikeOlderBuild(t *testing.T, manager *Manager, id string) string {
	t.Helper()
	ctx := context.Background()
	path, err := manager.Path(id)
	noErr(t, err)
	noErr(t, os.Mkdir(path, 0o700))
	noErr(t, manager.InitBareRepository(ctx, path, CreateOptions{}))
	noErr(t, manager.Store.Exec(ctx, `INSERT INTO repositories(id,name,description,created_at) VALUES(?,?,'',1)`, id, id))
	return path
}

// After the renamed directory was removed, only the deletion record remains.
// A directory that appears at the repository path meanwhile is not the
// deleted repository, and finishing the deletion must not move or remove it.
func TestFinishedRemovalNeverTouchesLaterDirectoryAtRepositoryPath(t *testing.T) {
	ctx := context.Background()
	manager, remote := newDeletionRepository(t)
	manager.deletionHook = func(step string) error {
		if step == "removed" {
			return errors.New("simulated crash")
		}
		return nil
	}
	if _, err := manager.Delete(ctx, "sample", DeleteFiles); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("interrupted deletion error=%v", err)
	}
	manager.deletionHook = nil
	noErr(t, os.Mkdir(remote, 0o700))
	noErr(t, manager.InitBareRepository(ctx, remote, CreateOptions{}))
	later := treeDigest(t, remote)
	noErr(t, manager.ReconcileDeletions(ctx))
	assertSameTree(t, later, treeDigest(t, remote))
	if deletions, err := manager.Store.RepositoryDeletions(ctx); err != nil || len(deletions) != 0 {
		t.Fatalf("intent remains %+v err=%v", deletions, err)
	}
}

// Before the move, a repository row for the name means the directory at the
// repository path belongs to that newer repository, so nothing is moved.
func TestPendingDeletionDoesNotMoveDirectoryOfRecordedRepository(t *testing.T) {
	ctx := context.Background()
	manager, remote := newDeletionRepository(t)
	root, err := manager.CanonicalStorageRoot()
	noErr(t, err)
	noErr(t, manager.Store.BeginRepositoryDeletion(ctx, state.RepositoryDeletion{
		RepositoryID: "sample", Mode: state.RepositoryDeletionKeepFiles, Root: root,
		Moved: ".owngit-removed/sample-20270115T080000Z.git", Marker: testMarkerToken, CreatedAt: testDeletionTime,
	}))
	noErr(t, writeDeletionMarker(root, "sample", testMarkerToken))
	noErr(t, manager.Store.Exec(ctx, `INSERT INTO repositories(id,name,description,created_at) VALUES('sample','sample','',1)`))
	before := treeDigest(t, remote)
	if err := manager.ReconcileDeletions(ctx); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("reconciliation error=%v", err)
	}
	assertSameTree(t, before, treeDigest(t, remote))
	if _, err := os.Lstat(filepath.Join(root, ".owngit-removed", "sample-20270115T080000Z.git")); !os.IsNotExist(err) {
		t.Fatalf("directory was moved: %v", err)
	}
}

// Deleting the new repository first finishes the old deletion, then deletes
// the repository the caller named.
func TestDeleteAfterNameReuseFinishesOldDeletionThenDeletesNewRepository(t *testing.T) {
	ctx := context.Background()
	manager, remote := newDeletionRepository(t)
	before := treeDigest(t, remote)
	manager.deletionHook = func(step string) error {
		if step == "moved" {
			return errors.New("simulated crash")
		}
		return nil
	}
	if _, err := manager.Delete(ctx, "sample", DeleteKeepFiles); !errors.Is(err, ErrDeleteIncomplete) {
		t.Fatalf("interrupted deletion error=%v", err)
	}
	manager.deletionHook = nil
	recordRepositoryLikeOlderBuild(t, manager, "sample")

	if _, err := manager.Delete(ctx, "sample", DeleteFiles); err != nil {
		t.Fatal(err)
	}
	assertRepositoryGone(t, manager, "sample")
	kept := filepath.Join(manager.RepositoryRoot(), ".owngit-removed", "sample-20270115T080000Z.git")
	assertSameTree(t, before, treeDigest(t, kept))
}

func TestDeleteNeverFollowsLinksOrLeavesTheRoot(t *testing.T) {
	ctx := context.Background()
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep.txt")
	noErr(t, os.WriteFile(sentinel, []byte("outside"), 0o600))
	assertOutsideIntact := func(t *testing.T) {
		t.Helper()
		if content, err := os.ReadFile(sentinel); err != nil || string(content) != "outside" {
			t.Fatalf("outside file changed: %q err=%v", content, err)
		}
	}
	symlink := func(t *testing.T, target, link string) {
		t.Helper()
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symbolic links are unavailable: %v", err)
		}
	}

	t.Run("repository path is a link", func(t *testing.T) {
		manager, remote := newDeletionRepository(t)
		moved := remote + ".real"
		noErr(t, os.Rename(remote, moved))
		symlink(t, outside, remote)
		for _, mode := range []DeleteMode{DeleteKeepFiles, DeleteFiles} {
			if _, err := manager.Delete(ctx, "sample", mode); err == nil {
				t.Fatalf("%s deleted through a link", mode)
			}
		}
		assertOutsideIntact(t)
		if _, exists, _ := manager.Store.Repository(ctx, "sample"); !exists {
			t.Fatal("refused deletion removed the record")
		}
	})

	t.Run("kept folder is a link", func(t *testing.T) {
		manager, remote := newDeletionRepository(t)
		symlink(t, outside, filepath.Join(manager.RepositoryRoot(), ".owngit-removed"))
		if _, err := manager.Delete(ctx, "sample", DeleteKeepFiles); err == nil {
			t.Fatal("kept a repository through a linked folder")
		}
		assertOutsideIntact(t)
		if entries, _ := os.ReadDir(outside); len(entries) != 1 {
			t.Fatalf("outside folder gained entries: %v", entries)
		}
		if _, err := os.Stat(remote); err != nil {
			t.Fatalf("repository moved: %v", err)
		}
	})

	t.Run("deletion target replaced by a link", func(t *testing.T) {
		manager, _ := newDeletionRepository(t)
		manager.deletionHook = func(step string) error {
			if step == "marked" {
				return errors.New("simulated crash")
			}
			return nil
		}
		if _, err := manager.Delete(ctx, "sample", DeleteFiles); !errors.Is(err, ErrDeleteIncomplete) {
			t.Fatalf("interrupted deletion error=%v", err)
		}
		manager.deletionHook = nil
		deletion, _, err := manager.Store.RepositoryDeletion(ctx, "sample")
		noErr(t, err)
		target := filepath.Join(manager.RepositoryRoot(), filepath.FromSlash(deletion.Moved))
		noErr(t, os.Rename(target, target+".real"))
		symlink(t, outside, target)
		if err := manager.ReconcileDeletions(ctx); !errors.Is(err, ErrDeleteIncomplete) {
			t.Fatalf("reconciliation followed a link: %v", err)
		}
		assertOutsideIntact(t)
		if _, err := os.Lstat(target); err != nil {
			t.Fatalf("link was removed: %v", err)
		}
	})

	t.Run("recorded target outside the deletion names", func(t *testing.T) {
		manager, remote := newDeletionRepository(t)
		root, err := manager.CanonicalStorageRoot()
		noErr(t, err)
		noErr(t, manager.Store.BeginRepositoryDeletion(ctx, state.RepositoryDeletion{
			RepositoryID: "sample", Mode: state.RepositoryDeletionDeleteFiles, Root: root,
			Moved: ".owngit-removed/other-20270115T080000Z.git", Marker: testMarkerToken, CreatedAt: testDeletionTime,
		}))
		if err := manager.ReconcileDeletions(ctx); !errors.Is(err, ErrDeleteIncomplete) {
			t.Fatalf("invalid target error=%v", err)
		}
		if _, err := os.Stat(remote); err != nil {
			t.Fatalf("repository with an invalid target was touched: %v", err)
		}
	})

	t.Run("repository folder changed", func(t *testing.T) {
		manager, remote := newDeletionRepository(t)
		noErr(t, manager.Store.BeginRepositoryDeletion(ctx, state.RepositoryDeletion{
			RepositoryID: "sample", Mode: state.RepositoryDeletionDeleteFiles, Root: outside,
			Moved: ".owngit-delete-" + strings.Repeat("0", 32), Marker: testMarkerToken, CreatedAt: testDeletionTime,
		}))
		if err := manager.ReconcileDeletions(ctx); !errors.Is(err, ErrDeleteIncomplete) {
			t.Fatalf("changed root error=%v", err)
		}
		if _, err := os.Stat(remote); err != nil {
			t.Fatalf("repository under a changed root was touched: %v", err)
		}
		assertOutsideIntact(t)
	})
}

func TestSetDefaultBranchSelectsExistingBranchOnly(t *testing.T) {
	ctx := context.Background()
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/master")
	oid := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, "", "--git-dir", remote, "update-ref", "refs/owngit/retained/heads/"+oid, oid)
	summary, err := manager.Summary(ctx, "sample")
	noErr(t, err)
	if summary.DefaultBranch != "main" || summary.DefaultOID != "" {
		t.Fatalf("fixture default=%+v", summary)
	}
	refsBefore := gitOutput(t, "", "--git-dir", remote, "for-each-ref")

	for _, name := range []string{"", "HEAD", "-x", "a b", "../x", "x..y", "x.lock", "missing", "refs/heads/missing", "bad\nname"} {
		if err := manager.SetDefaultBranch(ctx, "sample", name); !errors.Is(err, ErrBranchNotFound) {
			t.Fatalf("branch %q error=%v", name, err)
		}
	}
	if head := gitOutput(t, "", "--git-dir", remote, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("refused change moved HEAD to %q", head)
	}
	noErr(t, manager.SetDefaultBranch(ctx, "sample", "master"))
	summary, err = manager.Summary(ctx, "sample")
	noErr(t, err)
	if summary.DefaultBranch != "master" || summary.DefaultOID != oid {
		t.Fatalf("default after change=%+v", summary)
	}
	noErr(t, manager.SetDefaultBranch(ctx, "sample", "refs/heads/master"))
	if refsAfter := gitOutput(t, "", "--git-dir", remote, "for-each-ref"); refsAfter != refsBefore {
		t.Fatalf("default-branch change altered refs:\n%s\nwant\n%s", refsAfter, refsBefore)
	}
	if err := manager.SetDefaultBranch(ctx, "missing", "master"); !errors.Is(err, ErrRepositoryNotFound) {
		t.Fatalf("missing repository error=%v", err)
	}
}

// A Git failure while checking the name or the branch is a failure, never a
// report that the branch does not exist, and it leaves HEAD unchanged.
func TestSetDefaultBranchReportsGitFailuresAsFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing wrapper is a Unix test fixture")
	}
	ctx := context.Background()
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/other")
	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	for _, command := range []string{"check-ref-format", "show-ref"} {
		dir := t.TempDir()
		wrapper := filepath.Join(dir, "git")
		script := "#!/bin/sh\nfor a in \"$@\"; do if test \"$a\" = " + command + "; then echo 'fatal: simulated storage failure' >&2; exit 128; fi; done\nexec " + shellQuote(gitPath) + " \"$@\"\n"
		noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
		runner, err := gitexec.New(wrapper, filepath.Join(dir, "runtime"))
		noErr(t, err)
		manager.Git = runner
		err = manager.SetDefaultBranch(ctx, "sample", "other")
		if err == nil || errors.Is(err, ErrBranchNotFound) || !strings.Contains(err.Error(), "simulated storage failure") {
			t.Fatalf("%s failure reported as %v", command, err)
		}
	}
	if head := gitOutput(t, "", "--git-dir", remote, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("a failed change moved HEAD to %q", head)
	}
}

// A Git operation that holds the repository makes the change wait only
// briefly, then report the repository in use with HEAD unchanged.
func TestSetDefaultBranchWaitsOnlyBrieflyForTheRepository(t *testing.T) {
	ctx := context.Background()
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/other")
	lock := manager.Locks.For("sample")
	lock.RLock()
	defer lock.RUnlock()

	started := time.Now()
	err := manager.SetDefaultBranch(ctx, "sample", "other")
	if elapsed := time.Since(started); !errors.Is(err, ErrRepositoryInUse) || !errors.Is(err, ErrRepositoryBusy) || elapsed > deleteLockWait+time.Second {
		t.Fatalf("change while held: err=%v after %v", err, elapsed)
	}
	if head := gitOutput(t, "", "--git-dir", remote, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("a refused change moved HEAD to %q", head)
	}
}

// A start with the storage not mounted sees an empty folder at the repository
// root. The deletion must stay recorded and leave that folder untouched, and
// it completes once the storage is back. The deleted repository is the only
// one, so no other startup check notices the missing storage.
func TestUnmountedStorageKeepsUnfinishedDeletion(t *testing.T) {
	for _, test := range []struct {
		mode DeleteMode
		step string
	}{
		{DeleteFiles, "recorded"}, {DeleteFiles, "marked"}, {DeleteKeepFiles, "recorded"},
	} {
		t.Run(string(test.mode)+" after "+test.step, func(t *testing.T) {
			ctx := context.Background()
			manager, remote := newDeletionRepository(t)
			manager.deletionHook = func(step string) error {
				if step == test.step {
					return errors.New("simulated crash")
				}
				return nil
			}
			if _, err := manager.Delete(ctx, "sample", test.mode); !errors.Is(err, ErrDeleteIncomplete) {
				t.Fatalf("interrupted deletion error=%v", err)
			}
			manager.deletionHook = nil
			root := manager.RepositoryRoot()
			mounted := root + ".mounted"
			noErr(t, os.Rename(root, mounted))
			noErr(t, os.Mkdir(root, 0o700))

			err := manager.ReconcileDeletions(ctx)
			if !errors.Is(err, ErrDeleteIncomplete) || !strings.Contains(err.Error(), "storage may be unavailable") {
				t.Fatalf("reconciliation on an empty mount point error=%v", err)
			}
			if deletions, err := manager.Store.RepositoryDeletions(ctx); err != nil || len(deletions) != 1 {
				t.Fatalf("intent was dropped %+v err=%v", deletions, err)
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatalf("empty mount point was written %v err=%v", entries, err)
			}

			noErr(t, os.Remove(root))
			noErr(t, os.Rename(mounted, root))
			noErr(t, manager.ReconcileDeletions(ctx))
			if _, err := os.Lstat(remote); !os.IsNotExist(err) {
				t.Fatalf("repository path remains: %v", err)
			}
			entries, err := os.ReadDir(root)
			noErr(t, err)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), deletingDirectoryPrefix) || strings.HasPrefix(entry.Name(), deletionMarkerPrefix) {
					t.Fatalf("%s remains after the deletion finished", entry.Name())
				}
			}
			if deletions, err := manager.Store.RepositoryDeletions(ctx); err != nil || len(deletions) != 0 {
				t.Fatalf("intent remains %+v err=%v", deletions, err)
			}
		})
	}
}
