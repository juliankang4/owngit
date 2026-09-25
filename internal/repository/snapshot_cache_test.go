package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
)

// wroteRefs takes and releases the repository write lock, as every OwnGit
// ref writer does. Fixtures that change refs with Git directly call it, since
// a change that bypasses OwnGit is not seen until OwnGit next writes.
func wroteRefs(manager *Manager, id string) {
	lock := manager.Locks.For(id)
	lock.Lock()
	lock.Unlock()
}

// countGitProcesses replaces the manager's runner with one that records one
// line per Git process. A process with an argument matching the shell case
// pattern commands fails as a Git fatal error does (status 128) while
// failPath exists, and sleeps two seconds first while slowPath exists.
func countGitProcesses(t *testing.T, manager *Manager, commands string) (count func() int, failPath, slowPath string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace")
	failPath = filepath.Join(dir, "fail")
	slowPath = filepath.Join(dir, "slow")
	wrapper := filepath.Join(dir, "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(tracePath) + "\n" +
		"for a in \"$@\"; do case \"$a\" in " + commands + ")\n" +
		"  if [ -e " + shellQuote(failPath) + " ]; then echo 'fatal: simulated storage failure' >&2; exit 128; fi\n" +
		"  if [ -e " + shellQuote(slowPath) + " ]; then : > " + shellQuote(slowPath+".started") + "; /bin/sleep 2; fi;;\n" +
		"esac; done\n" +
		"exec " + shellQuote(gitPath) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	runner, err := gitexec.New(wrapper, filepath.Join(dir, "runtime"))
	noErr(t, err)
	manager.Git = runner
	noErr(t, os.WriteFile(tracePath, nil, 0o600))
	return func() int {
		t.Helper()
		trace, err := os.ReadFile(tracePath)
		noErr(t, err)
		return strings.Count(string(trace), "\n")
	}, failPath, slowPath
}

// cancelWhenSlowStarts returns a context that is canceled as soon as the slow
// command of countGitProcesses starts. The cancellation therefore hits that
// command and never the reads before it, however loaded the host is; a fixed
// deadline covered those reads too and failed on a busy machine. started
// reports whether the slow command ran at all.
func cancelWhenSlowStarts(t *testing.T, slowPath string) (ctx context.Context, started func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	marker := slowPath + ".started"
	stop := make(chan struct{})
	go func() {
		defer cancel()
		for {
			if _, err := os.Stat(marker); err == nil {
				return
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		cancel()
	})
	return ctx, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}
}

func mustSnapshot(t *testing.T, manager *Manager) RefSnapshot {
	t.Helper()
	snapshot, err := manager.RefSnapshot(context.Background(), "sample")
	noErr(t, err)
	return snapshot
}

// TestRefSnapshotCacheStartsNoGitWhileUnchanged proves that a second read
// with an unchanged write generation starts no Git process, and that the
// next OwnGit write makes the following read list the refs again.
func TestRefSnapshotCacheStartsNoGitWhileUnchanged(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the command-counting wrapper is a POSIX-shell fixture")
	}
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	first := gitOutput(t, "", "--git-dir", remote, "rev-parse", "main")
	count, _, _ := countGitProcesses(t, manager, "*")

	cold := mustSnapshot(t, manager)
	if cold.Summary.DefaultOID != first || !cold.HeadFound || count() == 0 {
		t.Fatalf("cold snapshot=%+v after %d Git processes", cold.Summary, count())
	}
	before := count()
	warm := mustSnapshot(t, manager)
	if started := count() - before; started != 0 {
		t.Fatalf("an unchanged repository started %d Git processes", started)
	}
	if !reflect.DeepEqual(warm, cold) {
		t.Fatalf("warm snapshot=%+v, want %+v", warm, cold)
	}

	// A write that bypasses OwnGit is not seen until OwnGit next writes.
	commitFile(t, work, "two", "two", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	second := gitOutput(t, "", "--git-dir", remote, "rev-parse", "main")
	if bypassed := mustSnapshot(t, manager); bypassed.Summary.DefaultOID != first {
		t.Fatalf("snapshot changed without an OwnGit write: %+v", bypassed.Summary)
	}
	wroteRefs(manager, "sample")
	before = count()
	if fresh := mustSnapshot(t, manager); fresh.Summary.DefaultOID != second || fresh.Head.OID != second {
		t.Fatalf("snapshot after a write=%+v, want main at %s", fresh.Summary, second)
	}
	if count() == before {
		t.Fatal("the read after a write started no Git process")
	}
}

// TestRefSnapshotCacheReturnsIndependentCopies proves that a caller changing
// a returned snapshot changes neither the cache nor another caller's copy.
func TestRefSnapshotCacheReturnsIndependentCopies(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "tag", "v1")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "refs/tags/v1")
	wroteRefs(manager, "sample")
	original := mustSnapshot(t, manager)
	want := mustSnapshot(t, manager)
	original.Summary.Branches[0].Name = "changed"
	original.Summary.Tags[0].OID = "changed"
	original.Summary.Branches = append(original.Summary.Branches, Ref{Name: "added"})
	if got := mustSnapshot(t, manager); !reflect.DeepEqual(got, want) {
		t.Fatalf("a caller's change reached the cache: %+v, want %+v", got.Summary, want.Summary)
	}
}

// TestRefSnapshotCacheReportsErrorsWithoutCachingThem proves that a missing
// repository is reported even with a cached snapshot, and that a failed read
// is not cached.
func TestRefSnapshotCacheReportsErrorsWithoutCachingThem(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the command-counting wrapper is a POSIX-shell fixture")
	}
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	wroteRefs(manager, "sample")
	count, failPath, _ := countGitProcesses(t, manager, "*")
	cached := mustSnapshot(t, manager)

	moved := remote + ".moved"
	noErr(t, os.Rename(remote, moved))
	if _, err := manager.RefSnapshot(context.Background(), "sample"); err == nil {
		t.Fatal("a missing repository folder served its cached snapshot")
	}
	noErr(t, os.Rename(moved, remote))
	if again := mustSnapshot(t, manager); !reflect.DeepEqual(again, cached) {
		t.Fatalf("snapshot after the folder returned=%+v, want %+v", again.Summary, cached.Summary)
	}

	wroteRefs(manager, "sample")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	if _, err := manager.RefSnapshot(context.Background(), "sample"); err == nil || !strings.Contains(err.Error(), "simulated storage failure") {
		t.Fatalf("failed read err=%v", err)
	}
	noErr(t, os.Remove(failPath))
	before := count()
	if recovered := mustSnapshot(t, manager); !reflect.DeepEqual(recovered, cached) || count() == before {
		t.Fatalf("read after a failure=%+v with %d new Git processes; a failure must not be cached", recovered.Summary, count()-before)
	}
}

// TestRefSnapshotCacheKeepsTheNewerSnapshot proves that a snapshot read at an
// older generation, stored late, never replaces one read at a newer
// generation.
func TestRefSnapshotCacheKeepsTheNewerSnapshot(t *testing.T) {
	var cache snapshotCache
	lock := gitexec.NewLocks().For("sample")
	lock.Lock()
	lock.Unlock()
	path := filepath.Join("root", "sample.git")
	older := RefSnapshot{Summary: Summary{DefaultBranch: "main", DefaultOID: "older"}}
	newer := RefSnapshot{Summary: Summary{DefaultBranch: "main", DefaultOID: "newer"}}
	cache.store("sample", path, lock, 1, newer)
	cache.store("sample", path, lock, 0, older)
	cache.store("sample", path, lock, 1, older)
	if got, ok := cache.lookup("sample", path, lock); !ok || got.Summary.DefaultOID != "newer" {
		t.Fatalf("lookup=%+v ok=%v, want the newer snapshot", got.Summary, ok)
	}
	// Once another write is released, the stored snapshot is no longer
	// current and the next read may replace it.
	lock.Lock()
	lock.Unlock()
	if _, ok := cache.lookup("sample", path, lock); ok {
		t.Fatal("a snapshot older than the lock generation was served")
	}
	cache.store("sample", path, lock, 2, older)
	if got, ok := cache.lookup("sample", path, lock); !ok || got.Summary.DefaultOID != "older" {
		t.Fatalf("lookup after a newer read=%+v ok=%v", got.Summary, ok)
	}
}

// TestRefSnapshotCacheForgetsRepositoriesAndRoots proves that entries are
// dropped for repositories no longer listed and when another storage root is
// read, and that a lock of another Locks value never matches.
func TestRefSnapshotCacheForgetsRepositoriesAndRoots(t *testing.T) {
	var cache snapshotCache
	locks := gitexec.NewLocks()
	one, two := locks.For("one"), locks.For("two")
	snapshot := RefSnapshot{Summary: Summary{DefaultBranch: "main"}}
	cache.store("one", filepath.Join("old", "one.git"), one, 0, snapshot)
	cache.store("two", filepath.Join("old", "two.git"), two, 0, snapshot)
	cache.forget([]string{"two"})
	if _, ok := cache.lookup("one", filepath.Join("old", "one.git"), one); ok {
		t.Fatal("a repository missing from the list kept its snapshot")
	}
	if _, ok := cache.lookup("two", filepath.Join("old", "two.git"), two); !ok {
		t.Fatal("a listed repository lost its snapshot")
	}
	if _, ok := cache.lookup("two", filepath.Join("new", "two.git"), two); ok {
		t.Fatal("a snapshot of another root was served")
	}
	cache.store("one", filepath.Join("new", "one.git"), one, 0, snapshot)
	if len(cache.entries) != 1 {
		t.Fatalf("entries after a root change=%d, want only the new root's", len(cache.entries))
	}
	if _, ok := cache.lookup("one", filepath.Join("new", "one.git"), gitexec.NewLocks().For("one")); ok {
		t.Fatal("a snapshot matched the lock of another Locks value")
	}
}

// TestRefSnapshotFollowsRepositoryWrites proves that restore, default-branch
// change, and deletion followed by re-creation are visible on the next read.
func TestRefSnapshotFollowsRepositoryWrites(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	ctx := context.Background()
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	source := gitOutput(t, work, "rev-parse", "HEAD")
	commitFile(t, work, "two", "two", "2024-01-02T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/other")
	wroteRefs(manager, "sample")
	target := mustSnapshot(t, manager).Summary.DefaultOID

	preview, err := manager.PreviewRestore(ctx, "sample", RestoreRequest{Source: source, Target: "main", Mode: RestoreAll})
	noErr(t, err)
	if mustSnapshot(t, manager).Summary.DefaultOID != target {
		t.Fatal("a restore preview changed the snapshot")
	}
	restored, err := manager.ApplyRestore(ctx, "sample", RestoreRequest{Source: source, Target: "main", Mode: RestoreAll, ExpectedHead: preview.ExpectedHead})
	noErr(t, err)
	if got := mustSnapshot(t, manager); got.Summary.DefaultOID != restored.CommitOID || got.Head.OID != restored.CommitOID || !strings.HasPrefix(got.Head.Subject, "Restore tree from ") {
		t.Fatalf("snapshot after restore=%+v head=%+v, want main at %s", got.Summary, got.Head, restored.CommitOID)
	}

	noErr(t, manager.SetDefaultBranch(ctx, "sample", "other"))
	if got := mustSnapshot(t, manager); got.Summary.DefaultBranch != "other" || got.Head.OID != gitOutput(t, "", "--git-dir", remote, "rev-parse", "other") {
		t.Fatalf("snapshot after default-branch change=%+v", got.Summary)
	}

	_, err = manager.Delete(ctx, "sample", DeleteFiles)
	noErr(t, err)
	if _, ok := manager.snapshots.entries["sample"]; ok {
		t.Fatal("deletion kept the repository's snapshot")
	}
	if _, err := manager.RefSnapshot(ctx, "sample"); err == nil {
		t.Fatal("a deleted repository reported a snapshot")
	}
	_, err = manager.Create(ctx, "sample", "")
	noErr(t, err)
	if got := mustSnapshot(t, manager); !got.Summary.Empty || got.HeadFound || len(got.Summary.Branches) != 0 {
		t.Fatalf("re-created repository snapshot=%+v, want empty", got.Summary)
	}
}

// TestRefSnapshotCacheSkipsSnapshotsWithAFailedFollowUpRead proves that a
// snapshot whose git log or symbolic-ref read failed or was canceled is
// returned without those fields but not cached, so the next call reads again
// and shows the complete result.
func TestRefSnapshotCacheSkipsSnapshotsWithAFailedFollowUpRead(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the command-counting wrapper is a POSIX-shell fixture")
	}
	// completeAfter checks that the next call reads again, shows want, and is
	// then cached.
	completeAfter := func(t *testing.T, manager *Manager, count func() int, want func(RefSnapshot) bool) {
		t.Helper()
		before := count()
		complete := mustSnapshot(t, manager)
		if !want(complete) || count() == before {
			t.Fatalf("snapshot after the failure ended=%+v head=%+v, read with %d Git processes", complete.Summary, complete.Head, count()-before)
		}
		before = count()
		if cached := mustSnapshot(t, manager); !reflect.DeepEqual(cached, complete) || count() != before {
			t.Fatalf("complete snapshot was not cached: %d Git processes", count()-before)
		}
	}

	t.Run("head metadata fails", func(t *testing.T) {
		manager, _, work := newTestRepository(t)
		// A doubled space makes for-each-ref's subject differ from git log's,
		// so the head is read again with git log.
		commitFile(t, work, "one", "Fix  spacing", "2024-01-01T00:00:00Z")
		runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
		wroteRefs(manager, "sample")
		count, failPath, _ := countGitProcesses(t, manager, "log")
		noErr(t, os.WriteFile(failPath, nil, 0o600))
		if degraded := mustSnapshot(t, manager); degraded.HeadFound {
			t.Fatalf("failed git log still reported a head: %+v", degraded.Head)
		}
		noErr(t, os.Remove(failPath))
		completeAfter(t, manager, count, func(snapshot RefSnapshot) bool {
			return snapshot.HeadFound && snapshot.Head.Subject == "Fix  spacing" && snapshot.Head.CommitterName == "Test Author"
		})
	})

	t.Run("head metadata canceled", func(t *testing.T) {
		manager, _, work := newTestRepository(t)
		commitFile(t, work, "one", "Fix  spacing", "2024-01-01T00:00:00Z")
		runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
		wroteRefs(manager, "sample")
		count, _, slowPath := countGitProcesses(t, manager, "log")
		noErr(t, os.WriteFile(slowPath, nil, 0o600))
		canceled, started := cancelWhenSlowStarts(t, slowPath)
		degraded, err := manager.RefSnapshot(canceled, "sample")
		if !started() {
			t.Fatal("the slow git log never started")
		}
		if err != nil || degraded.HeadFound {
			t.Fatalf("canceled git log: snapshot head=%+v err=%v, want the refs without a head", degraded.Head, err)
		}
		noErr(t, os.Remove(slowPath))
		completeAfter(t, manager, count, func(snapshot RefSnapshot) bool {
			return snapshot.HeadFound && snapshot.Head.Subject == "Fix  spacing" && snapshot.Head.CommitterName == "Test Author"
		})
	})

	t.Run("symbolic-ref fails", func(t *testing.T) {
		// An empty repository has no branch marked as HEAD, so its default
		// branch name comes from symbolic-ref.
		manager, _, _ := newTestRepository(t)
		count, failPath, _ := countGitProcesses(t, manager, "symbolic-ref")
		noErr(t, os.WriteFile(failPath, nil, 0o600))
		if degraded := mustSnapshot(t, manager); degraded.Summary.DefaultBranch != "" {
			t.Fatalf("failed symbolic-ref still named a default branch: %+v", degraded.Summary)
		}
		noErr(t, os.Remove(failPath))
		completeAfter(t, manager, count, func(snapshot RefSnapshot) bool {
			return snapshot.Summary.DefaultBranch == "main" && snapshot.Summary.Empty
		})
	})

	t.Run("symbolic-ref canceled", func(t *testing.T) {
		manager, _, _ := newTestRepository(t)
		count, _, slowPath := countGitProcesses(t, manager, "symbolic-ref")
		noErr(t, os.WriteFile(slowPath, nil, 0o600))
		canceled, started := cancelWhenSlowStarts(t, slowPath)
		degraded, err := manager.RefSnapshot(canceled, "sample")
		if !started() {
			t.Fatal("the slow symbolic-ref never started")
		}
		if err != nil || degraded.Summary.DefaultBranch != "" {
			t.Fatalf("canceled symbolic-ref: summary=%+v err=%v", degraded.Summary, err)
		}
		noErr(t, os.Remove(slowPath))
		completeAfter(t, manager, count, func(snapshot RefSnapshot) bool {
			return snapshot.Summary.DefaultBranch == "main"
		})
	})
}

// TestRefSnapshotCacheKeepsADetachedHead proves that symbolic-ref's answer
// that HEAD is not a symbolic ref is complete, so a detached HEAD is cached.
func TestRefSnapshotCacheKeepsADetachedHead(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the command-counting wrapper is a POSIX-shell fixture")
	}
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, "", "--git-dir", remote, "update-ref", "--no-deref", "HEAD", gitOutput(t, "", "--git-dir", remote, "rev-parse", "main"))
	wroteRefs(manager, "sample")
	count, _, _ := countGitProcesses(t, manager, "no-command")
	detached := mustSnapshot(t, manager)
	if detached.Summary.DefaultBranch != "" || detached.HeadFound || len(detached.Summary.Branches) != 1 {
		t.Fatalf("detached HEAD snapshot=%+v", detached.Summary)
	}
	trace := count()
	if trace != 2 {
		t.Fatalf("detached HEAD read used %d Git processes, want for-each-ref and symbolic-ref", trace)
	}
	if cached := mustSnapshot(t, manager); !reflect.DeepEqual(cached, detached) || count() != trace {
		t.Fatalf("a detached HEAD snapshot was not cached: %d more Git processes", count()-trace)
	}
}
