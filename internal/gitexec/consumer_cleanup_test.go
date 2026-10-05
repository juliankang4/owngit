package gitexec_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// consumerRepository is one recorded repository served by a real Git.
type consumerRepository struct {
	git     *gitexec.Runner
	manager *repository.Manager
	id      string
	path    string
}

func newConsumerRepository(t *testing.T) *consumerRepository {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	store, err := state.Open(ctx, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	git, err := gitexec.New("", filepath.Join(root, "runtime"))
	if err != nil {
		t.Skip("Git is unavailable:", err)
	}
	repositoryRoot := filepath.Join(root, "repositories")
	if err := os.Mkdir(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteSetup(ctx, repositoryRoot, "open", "", "test-admin-hash", true); err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: git, Locks: gitexec.NewLocks(), Root: repositoryRoot}
	stored, err := manager.Create(ctx, "cleanup", "")
	if err != nil {
		t.Fatal(err)
	}
	path, err := manager.Path(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &consumerRepository{git: git, manager: manager, id: stored.ID, path: path}
}

// run runs one Git command in the repository and returns its trimmed output.
func (r *consumerRepository) run(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	result, err := r.git.RunWithEnvironment(context.Background(), r.path, strings.NewReader(stdin), []string{
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	}, append([]string{"--git-dir", "."}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(result.Stdout))
}

// commit writes files as one commit on parents without a work tree.
func (r *consumerRepository) commit(t *testing.T, files map[string]string, parents ...string) string {
	t.Helper()
	var tree strings.Builder
	for name, content := range files {
		tree.WriteString("100644 blob " + r.run(t, content, "hash-object", "-w", "--stdin") + "\t" + name + "\n")
	}
	args := []string{"commit-tree", r.run(t, tree.String(), "mktree"), "-m", "files"}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	return r.run(t, "", args...)
}

// holdGit makes every later Git command named held sleep instead of running,
// so the caller's own time limit stops it.
func (r *consumerRepository) holdGit(t *testing.T, held string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the slow Git wrapper is a POSIX shell script")
	}
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\ncase \" $* \" in *\" " + held + " \"*) sleep 60;; esac\nexec '" + r.git.GitPath + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r.git.GitPath = wrapper
}

// refusesCleanupFailureAtLimit injects a termination failure after the real
// one and requires read, stopped by its own time limit, to return it. Later
// commands are cleaned up normally.
func refusesCleanupFailureAtLimit(t *testing.T, git *gitexec.Runner, read func() (any, error)) {
	t.Helper()
	terminateErr := errors.New("injected termination failure")
	realFailures := gitexec.InjectCleanupFaults(git, 0, terminateErr, nil)
	result, err := read()
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, gitexec.ErrProcessCleanup) || !errors.Is(err, terminateErr) {
		t.Fatalf("time limit with cleanup failure = %+v err=%v", result, err)
	}
	if err := realFailures(); err != nil {
		t.Fatalf("real cleanup failed: %v", err)
	}
	gitexec.InjectCleanupFaults(git, 0, nil, nil)
}

// Repository reads that accept a truncated prefix or an exit status as an
// answer refuse both when the process cleanup failed.
func TestRepositoryReadsRefuseACleanupFailure(t *testing.T) {
	ctx := context.Background()
	repo := newConsumerRepository(t)
	entry := repository.TreeEntry{OID: repo.run(t, "0123456789abcdef", "hash-object", "-w", "--stdin"), Type: "blob", Size: -1}

	// Controls: a cut read is a usable prefix, and an unset key is Git's answer.
	if blob, err := repo.manager.BlobAt(ctx, repo.id, entry, 4); err != nil || string(blob.Content) != "0123" || !blob.Truncated {
		t.Fatalf("prefix read = %q truncated=%v err=%v", blob.Content, blob.Truncated, err)
	}
	if format, err := repo.manager.ObjectFormat(ctx, repo.path); err != nil || format != repository.ObjectFormatSHA1 {
		t.Fatalf("object format = %q err=%v", format, err)
	}

	closeErr := errors.New("injected owner release failure")
	realFailures := gitexec.InjectCleanupFaults(repo.git, 0, nil, closeErr)
	if blob, err := repo.manager.BlobAt(ctx, repo.id, entry, 4); !errors.Is(err, gitexec.ErrProcessCleanup) || !errors.Is(err, closeErr) {
		t.Fatalf("prefix read after cleanup failure = %q err=%v", blob.Content, err)
	}
	if format, err := repo.manager.ObjectFormat(ctx, repo.path); !errors.Is(err, closeErr) {
		t.Fatalf("object format after cleanup failure = %q err=%v", format, err)
	}
	if err := realFailures(); err != nil {
		t.Fatalf("real cleanup failed: %v", err)
	}
}

// A language count keeps a timed-out count and unreadable attributes as
// results. A cleanup failure on the same paths is returned and not kept.
func TestLanguageCountRefusesACleanupFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 2 second language count limit twice")
	}
	ctx := context.Background()

	t.Run("attributes", func(t *testing.T) {
		repo := newConsumerRepository(t)
		commit := repo.commit(t, map[string]string{
			".gitattributes": "*.inc linguist-language=PHP\n", "main.go": "package main\n", "lib.inc": "<?php\n",
		})
		// The count lists the tree, then reads the attributes: fail only the second.
		closeErr := errors.New("injected owner release failure")
		realFailures := gitexec.InjectCleanupFaults(repo.git, 1, nil, closeErr)
		if stats, err := repo.manager.Languages(ctx, repo.id, commit); !errors.Is(err, gitexec.ErrProcessCleanup) || !errors.Is(err, closeErr) {
			t.Fatalf("attributes after cleanup failure = %+v err=%v", stats, err)
		}
		if err := realFailures(); err != nil {
			t.Fatalf("real cleanup failed: %v", err)
		}
		gitexec.InjectCleanupFaults(repo.git, 0, nil, nil)
		stats, err := repo.manager.Languages(ctx, repo.id, commit)
		if err != nil || stats.Attributes != repository.AttributesApplied || len(stats.Shares) != 2 {
			t.Fatalf("count after the failure = %+v err=%v, want a fresh count with applied attributes", stats, err)
		}
	})

	// The wrapper holds one command past the count's time limit: the tree
	// listing, or the attribute read after it.
	for _, held := range []string{"ls-tree", "check-attr"} {
		t.Run("time limit in "+held, func(t *testing.T) {
			repo := newConsumerRepository(t)
			commit := repo.commit(t, map[string]string{".gitattributes": "*.inc linguist-language=PHP\n", "main.go": "package main\n"})
			repo.holdGit(t, held)
			refusesCleanupFailureAtLimit(t, repo.git, func() (any, error) { return repo.manager.Languages(ctx, repo.id, commit) })
			// Control: the same limit with a clean stop is a timed-out result.
			if stats, err := repo.manager.Languages(ctx, repo.id, commit); err != nil || !stats.TimedOut {
				t.Fatalf("time limit with a clean stop = %+v err=%v", stats, err)
			}
		})
	}
}

// A diff stopped by the comparison's own time limit is shown as incomplete
// only when the stopped Git was cleaned up.
func TestCompareTimeLimitRefusesACleanupFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 20-second comparison limit")
	}
	ctx := context.Background()
	repo := newConsumerRepository(t)
	base := repo.commit(t, map[string]string{"shared.txt": "shared\n"})
	source := repo.commit(t, map[string]string{"shared.txt": "shared\n", "a.txt": "a\n"}, base)
	target := repo.commit(t, map[string]string{"shared.txt": "moved\n"}, base)
	repo.holdGit(t, "diff")
	refusesCleanupFailureAtLimit(t, repo.git, func() (any, error) {
		return repo.manager.Compare(ctx, repo.id, target, source, state.DefaultBrowseLimits.CompareBytes, state.DefaultBrowseLimits.CompareTime)
	})
}
