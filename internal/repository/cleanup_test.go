package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Unused object cleanup removes only unreachable objects older than the
// grace period, loose and packed, in SHA-1 and SHA-256 repositories. It
// keeps every object a ref reaches, kept history and pull request refs
// included, younger unreachable objects and packs with a .keep file. Off,
// full maintenance removes nothing.
func TestCleanupRemovesOnlyOldUnreachableObjects(t *testing.T) {
	for _, format := range []string{ObjectFormatSHA1, ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			manager, _, _ := newTestRepository(t)
			_, err := manager.CreateWithOptions(ctx, "cleanup", "", CreateOptions{ObjectFormat: format})
			noErr(t, err)
			remote, err := manager.Path("cleanup")
			noErr(t, err)
			work := filepath.Join(t.TempDir(), "work")
			runGit(t, "", "init", "--object-format="+format, "--initial-branch=main", work)
			runGit(t, work, "config", "user.name", "Test Author")
			runGit(t, work, "config", "user.email", "test@example.invalid")
			runGit(t, work, "remote", "add", "origin", remote)
			for index := range 3 {
				commitFile(t, work, "content "+string(rune('a'+index))+"\n", "commit", "2024-01-01T00:00:00Z")
				runGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
			}
			forced := gitOutput(t, work, "rev-parse", "HEAD")
			runGit(t, work, "push", "-q", "--force", "origin", "HEAD~1:refs/heads/main")
			pull := gitInputOutput(t, remote, []byte("pull request only\n"), "hash-object", "-w", "--stdin")
			runGit(t, "", "--git-dir", remote, "update-ref", "refs/owngit/pull-requests/1/source", gitInputOutput(t, remote, nil, "mktree"))
			runGit(t, "", "--git-dir", remote, "update-ref", "refs/owngit/pull-requests/1/blob", pull)

			old := time.Now().Add(-30 * 24 * time.Hour)
			age := func(paths ...string) {
				for _, path := range paths {
					noErr(t, os.Chtimes(path, old, old))
				}
			}
			loosePath := func(oid string) string { return filepath.Join(remote, "objects", oid[:2], oid[2:]) }
			packPaths := func(id string, extensions ...string) []string {
				var paths []string
				for _, extension := range extensions {
					paths = append(paths, filepath.Join(remote, "objects", "pack", "pack-"+id+extension))
				}
				return paths
			}
			oldLoose := gitInputOutput(t, remote, []byte("old unreachable loose\n"), "hash-object", "-w", "--stdin")
			age(loosePath(oldLoose))
			oldPacked := gitInputOutput(t, remote, []byte("old unreachable packed\n"), "hash-object", "-w", "--stdin")
			packID := gitInputOutput(t, remote, []byte(oldPacked+"\n"), "pack-objects", "-q", filepath.Join(remote, "objects", "pack", "pack"))
			noErr(t, os.Remove(loosePath(oldPacked)))
			age(packPaths(packID, ".pack", ".idx")...)
			oldKept := gitInputOutput(t, remote, []byte("old unreachable in a kept pack\n"), "hash-object", "-w", "--stdin")
			keptID := gitInputOutput(t, remote, []byte(oldKept+"\n"), "pack-objects", "-q", filepath.Join(remote, "objects", "pack", "pack"))
			noErr(t, os.Remove(loosePath(oldKept)))
			noErr(t, os.WriteFile(packPaths(keptID, ".keep")[0], []byte("synthetic keep\n"), 0o600))
			age(packPaths(keptID, ".pack", ".idx", ".keep")...)
			// Full maintenance, with cleanup off, removes nothing.
			schedule := MaintenanceSchedule{}.withDefaults()
			full := inventory(t, remote)
			_, err = manager.maintain(ctx, "cleanup", MaintenanceFull, schedule)
			noErr(t, err, "full maintenance with cleanup off")
			assertSameInventory(t, full, inventory(t, remote))
			recent := gitInputOutput(t, remote, []byte("recent unreachable\n"), "hash-object", "-w", "--stdin")
			// Everything else, reachable or not, is old, so only
			// reachability keeps what a ref reaches.
			noErr(t, filepath.WalkDir(filepath.Join(remote, "objects"), func(path string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() && path != loosePath(recent) {
					err = os.Chtimes(path, old, old)
				}
				return err
			}))
			exists := func(oid string) bool {
				_, err := gitCombined("", "--git-dir", remote, "cat-file", "-e", oid)
				return err == nil
			}
			before := inventory(t, remote)

			schedule.cleanupGrace = 14 * 24 * time.Hour
			// An unfinished check job or an import in progress may still
			// need an unreachable commit, so cleanup waits for them.
			admitCheckJob(t, manager, "cleanup", "job", oldLoose)
			noErr(t, manager.Store.Exec(ctx, `INSERT INTO import_runs(id,repository_id,source_generation,authority_revision,kind,status,started_at,created_at)
				VALUES('run','cleanup',1,1,'refresh','fetching',1,1)`))
			for _, finish := range []string{
				`UPDATE check_jobs SET status='claimed' WHERE id='job'`,
				`UPDATE check_jobs SET status='started' WHERE id='job'`,
				`UPDATE check_jobs SET status='passed' WHERE id='job'`,
				`UPDATE import_runs SET status='publishing' WHERE id='run'`,
				`UPDATE import_runs SET status='complete' WHERE id='run'`,
			} {
				if _, err := manager.maintain(ctx, "cleanup", MaintenanceCleanup, schedule); !errors.Is(err, errCleanupDeferred) || !exists(oldLoose) || !exists(oldPacked) {
					t.Fatalf("cleanup with an unfinished consumer: err=%v", err)
				}
				noErr(t, manager.Store.Exec(ctx, finish))
			}
			steps, err := manager.maintain(ctx, "cleanup", MaintenanceCleanup, schedule)
			noErr(t, err, "cleanup")
			if steps != 4 {
				t.Fatalf("cleanup ran %d steps", steps)
			}
			if exists(oldLoose) || exists(oldPacked) {
				t.Fatalf("old unreachable objects stayed: loose=%v packed=%v", exists(oldLoose), exists(oldPacked))
			}
			for name, oid := range map[string]string{"recent": recent, "kept pack": oldKept, "kept history": forced, "pull request": pull} {
				if !exists(oid) {
					t.Fatalf("cleanup removed the %s object %s", name, oid)
				}
			}
			if _, err := os.Stat(packPaths(keptID, ".keep")[0]); err != nil {
				t.Fatalf("the .keep file: %v", err)
			}
			after := inventory(t, remote)
			if before.refs != after.refs || before.head != after.head {
				t.Fatalf("refs changed:\n%s\n%s", before.refs, after.refs)
			}
			runGit(t, "", "--git-dir", remote, "fsck", "--connectivity-only", "--no-dangling")
		})
	}
}

// admitCheckJob records a pending check job, as admission does, for oid.
func admitCheckJob(t *testing.T, manager *Manager, repositoryID, id, oid string) {
	t.Helper()
	ctx := context.Background()
	task, err := manager.Store.CreateTask(ctx, repositoryID, "check", time.Now())
	noErr(t, err)
	noErr(t, manager.Store.Exec(ctx, `INSERT INTO check_jobs(id,repository_id,task_id,trigger_kind,event_key,source_oid,trigger_ref,workflow_path,
		workflow_digest,configuration_version,executor,policy_version,consent_version,limits_json,execution_json,dedup_digest,status,admitted_at)
		VALUES(?,?,?,'push','event',?,'refs/heads/main','check.yml',?,1,'external_runner',1,1,'{}','{}',?,'pending',1)`,
		id, repositoryID, task.ID, oid, strings.Repeat("a", 64), strings.Repeat("b", 64)))
}

// A check that pinned its commit and is between reads keeps its source
// objects, although they are old and no ref reaches them any more: cleanup
// waits until the job ends, then removes them.
func TestCleanupKeepsTheSourceOfACheckBetweenReads(t *testing.T) {
	ctx := context.Background()
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "source only\n", "source", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	source := gitOutput(t, work, "rev-parse", "HEAD")
	old := time.Now().Add(-30 * 24 * time.Hour)
	noErr(t, filepath.WalkDir(filepath.Join(remote, "objects"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			err = os.Chtimes(path, old, old)
		}
		return err
	}))
	// The objects are old, but the commit becomes unreachable only now.
	runGit(t, "", "--git-dir", remote, "update-ref", "-d", "refs/heads/main")
	admitCheckJob(t, manager, "sample", "job", source)
	pinned, err := manager.PinRepository(ctx, "sample", source, source)
	noErr(t, err)
	entries, err := pinned.ListTreeRecursive(ctx, PinnedHead, 1<<20)
	noErr(t, err)
	if len(entries) != 1 {
		t.Fatalf("tree entries=%+v", entries)
	}

	schedule := MaintenanceSchedule{}.withDefaults()
	schedule.cleanupGrace = 2 * 24 * time.Hour
	if _, err := manager.maintain(ctx, "sample", MaintenanceCleanup, schedule); !errors.Is(err, errCleanupDeferred) {
		t.Fatalf("cleanup during the check: %v", err)
	}
	if _, err := pinned.ReadBlobObject(ctx, entries[0].OID, entries[0].Size); err != nil {
		t.Fatalf("the check's next read after cleanup: %v", err)
	}

	noErr(t, manager.Store.Exec(ctx, `UPDATE check_jobs SET status='passed' WHERE id='job'`))
	steps, err := manager.maintain(ctx, "sample", MaintenanceCleanup, schedule)
	noErr(t, err, "cleanup after the check")
	if steps != 4 {
		t.Fatalf("cleanup ran %d steps", steps)
	}
	if _, err := gitCombined("", "--git-dir", remote, "cat-file", "-e", source); err == nil {
		t.Fatal("cleanup kept the old unreachable commit after the check ended")
	}
}

// A check admission records its job under the repository read lock after
// verifying the source again, so it cannot slip in between cleanup reading
// the unfinished jobs and removing objects under the write lock. Admitted
// first, cleanup waits; cleaned first, the admission is refused.
func TestCheckAdmissionAndCleanupExcludeEachOther(t *testing.T) {
	ctx := context.Background()
	manager, remote, work := newTestRepository(t)
	schedule := MaintenanceSchedule{}.withDefaults()
	schedule.cleanupGrace = 2 * 24 * time.Hour
	oldUnreachable := func(name string) string {
		runGit(t, work, "checkout", "-q", "--orphan", name)
		commitFile(t, work, name+"\n", name, "2024-01-01T00:00:00Z")
		runGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/"+name)
		old := time.Now().Add(-30 * 24 * time.Hour)
		noErr(t, filepath.WalkDir(filepath.Join(remote, "objects"), func(path string, entry os.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				err = os.Chtimes(path, old, old)
			}
			return err
		}))
		runGit(t, "", "--git-dir", remote, "update-ref", "-d", "refs/heads/"+name)
		return gitOutput(t, work, "rev-parse", "HEAD")
	}
	present := func(oid string) bool {
		_, err := gitCombined("", "--git-dir", remote, "cat-file", "-e", oid)
		return err == nil
	}

	// Admitted first: cleanup sees the job and leaves the source.
	admitted := oldUnreachable("admitted")
	pinned, err := manager.PinRepository(ctx, "sample", admitted, admitted)
	noErr(t, err)
	noErr(t, pinned.WhilePresent(ctx, func() error {
		admitCheckJob(t, manager, "sample", "admitted", admitted)
		return nil
	}))
	if _, err := manager.maintain(ctx, "sample", MaintenanceCleanup, schedule); !errors.Is(err, errCleanupDeferred) || !present(admitted) {
		t.Fatalf("cleanup after an admission: err=%v present=%v", err, present(admitted))
	}
	noErr(t, manager.Store.Exec(ctx, `UPDATE check_jobs SET status='passed' WHERE id='admitted'`))

	// Cleanup first: while it holds the write lock, having found no
	// unfinished job, an admission cannot record one; once cleanup removed
	// the source, the admission is refused.
	late := oldUnreachable("late")
	pinned, err = manager.PinRepository(ctx, "sample", late, late)
	noErr(t, err)
	recorded := false
	record := func() error { recorded = true; return nil }
	lock := manager.Locks.For("sample")
	lock.Lock()
	err = pinned.WhilePresent(ctx, record)
	lock.UnlockWithoutRefChanges()
	if !errors.Is(err, ErrPinnedRepositoryBusy) || recorded {
		t.Fatalf("admission during cleanup's step: err=%v recorded=%v", err, recorded)
	}
	if _, err := manager.maintain(ctx, "sample", MaintenanceCleanup, schedule); err != nil || present(late) || present(admitted) {
		t.Fatalf("cleanup with no unfinished job: err=%v", err)
	}
	if err := pinned.WhilePresent(ctx, record); !errors.Is(err, ErrPinnedObjectUnavailable) || recorded {
		t.Fatalf("admission after cleanup: err=%v recorded=%v", err, recorded)
	}
}
