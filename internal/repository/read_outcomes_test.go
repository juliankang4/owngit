package repository

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/hostmem"
)

// readOutcomeRepository is a repository of the given object format with a
// commit on main, an annotated tag, a tag of that tag, tags of a tree and a
// blob, and the short name "odd" used both by a branch that points to the
// annotated tag and by a lightweight tag.
type readOutcomeRepository struct {
	manager                   *Manager
	id                        string
	commit, parent, tag, tree string
	missing                   string
	oddBranchPeel, oddTagPeel string
}

func newReadOutcomeRepository(t *testing.T, format string) readOutcomeRepository {
	t.Helper()
	manager, _, _ := newTestRepository(t)
	id := "formatted"
	_, err := manager.CreateWithOptions(context.Background(), id, "", CreateOptions{ObjectFormat: format})
	noErr(t, err)
	remote, err := manager.Path(id)
	noErr(t, err)
	work := filepath.Join(t.TempDir(), "work")
	runGit(t, "", "init", "--object-format="+format, "--initial-branch=main", work)
	runGit(t, work, "config", "user.name", "Test Author")
	runGit(t, work, "config", "user.email", "test@example.invalid")
	commitFile(t, work, "one", "one", "2024-01-01T00:00:00Z")
	parent := gitOutput(t, work, "rev-parse", "HEAD")
	commitFile(t, work, "two", "two", "2024-01-02T00:00:00Z")
	commit := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "tag", "-a", "-m", "annotated", "annotated", parent)
	runGit(t, work, "tag", "-a", "-m", "nested", "nested", "annotated")
	runGit(t, work, "tag", "-a", "-m", "of a tree", "tree-tag", "HEAD^{tree}")
	runGit(t, work, "tag", "-a", "-m", "of a blob", "blob-tag", "HEAD:file.txt")
	runGit(t, work, "tag", "odd", commit)
	runGit(t, work, "push", "-q", "--tags", remote, "HEAD:refs/heads/main")
	tag := gitOutput(t, work, "rev-parse", "refs/tags/annotated")
	// Git refuses to point a branch at a tag, but a ref file written
	// without Git can still do so.
	noErr(t, os.WriteFile(filepath.Join(remote, "refs", "heads", "odd"), []byte(tag+"\n"), 0o600))
	if got := gitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/odd"); got != tag {
		t.Fatalf("refs/heads/odd = %s, want the tag %s", got, tag)
	}
	wroteRefs(manager, id)
	return readOutcomeRepository{
		manager: manager, id: id, commit: commit, parent: parent, tag: tag,
		tree:          gitOutput(t, work, "rev-parse", "HEAD^{tree}"),
		missing:       strings.Repeat("e", len(commit)),
		oddBranchPeel: parent, oddTagPeel: commit,
	}
}

// Each read that names a branch, tag, commit or path either answers, or
// establishes that the object does not exist (ErrNotFound), or fails without
// telling. A failed Git process, a cancelled request and malformed Git output
// are failures, not absence, and no failure or absence is cached.
func TestReadsTellAbsenceFromFailure(t *testing.T) {
	requirePOSIX(t)
	// The file records, the object metadata and the line counts are three
	// processes only where the host can price a delta rebuild.
	knownMemoryCeiling(t)
	for _, format := range []string{ObjectFormatSHA1, ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			r := newReadOutcomeRepository(t, format)
			ctx := context.Background()
			m := r.manager
			mustSnapshot := func() {
				t.Helper()
				_, err := m.RefSnapshot(ctx, r.id)
				noErr(t, err)
			}
			mustSnapshot()
			wantFailure := func(name string, err error) {
				t.Helper()
				if err == nil || errors.Is(err, ErrNotFound) {
					t.Fatalf("%s = %v, want a failure that is not ErrNotFound", name, err)
				}
			}

			// Failures first, so no answer is cached yet.
			_, failPath, _ := countGitProcesses(t, m, "cat-file|log|merge-base")
			noErr(t, os.WriteFile(failPath, nil, 0o600))
			_, _, err := m.ResolveRef(ctx, r.id, "nested")
			wantFailure("ResolveRef(nested) with a failing peel", err)
			// The branch "odd" needs a peel. Its failure is not a reason to
			// answer with the tag of the same name.
			_, _, err = m.ResolveRef(ctx, r.id, "odd")
			wantFailure("ResolveRef(odd) with a failing peel", err)
			_, _, err = m.ResolveRevision(ctx, r.id, r.commit)
			wantFailure("ResolveRevision(commit) with a failing peel", err)
			_, _, err = m.CommitFiles(ctx, r.id, r.commit)
			wantFailure("CommitFiles with failing Git", err)
			_, err = m.CommitReachableFrom(ctx, r.id, r.commit, r.parent)
			wantFailure("CommitReachableFrom with failing Git", err)
			noErr(t, os.Remove(failPath))

			// Only the file read fails: the commit exists, so the failure
			// stays a failure.
			_, failPath, _ = countGitProcesses(t, m, "--raw")
			noErr(t, os.WriteFile(failPath, nil, 0o600))
			_, _, err = m.CommitFiles(ctx, r.id, r.commit)
			wantFailure("CommitFiles with a failing log of an existing commit", err)
			noErr(t, os.Remove(failPath))

			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			_, _, err = m.ResolveRef(cancelled, r.id, "nested")
			wantFailure("ResolveRef with a cancelled request", err)
			_, _, err = m.CommitFiles(cancelled, r.id, r.parent)
			wantFailure("CommitFiles with a cancelled request", err)

			malformedGit(t, m, "cat-file", "not an answer")
			_, _, err = m.ResolveRef(ctx, r.id, "nested")
			wantFailure("ResolveRef with malformed peel output", err)
			_, _, err = m.ResolveRevision(ctx, r.id, r.missing)
			wantFailure("ResolveRevision with malformed peel output", err)

			// Absence, and the answers, once Git works again.
			count, _, _ := countGitProcesses(t, m, "*")
			mustSnapshot()
			for _, check := range []struct{ requested, full, oid string }{
				{"nested", "refs/tags/nested", r.parent},
				{"odd", "refs/heads/odd", r.oddBranchPeel},
				{"refs/tags/odd", "refs/tags/odd", r.oddTagPeel},
				{"main", "refs/heads/main", r.commit},
			} {
				full, oid, err := m.ResolveRef(ctx, r.id, check.requested)
				if err != nil || full != check.full || oid != check.oid {
					t.Fatalf("ResolveRef(%q) = %q %q %v, want %q %s", check.requested, full, oid, err, check.full, check.oid)
				}
			}
			for _, requested := range []string{"missing", "tree-tag", "refs/tags/blob-tag", "refs/heads/nested", "bad..name"} {
				if _, _, err := m.ResolveRef(ctx, r.id, requested); !errors.Is(err, ErrNotFound) {
					t.Fatalf("ResolveRef(%q) = %v, want ErrNotFound", requested, err)
				}
			}
			if full, oid, err := m.ResolveRevision(ctx, r.id, r.commit); err != nil || full != r.commit || oid != r.commit {
				t.Fatalf("ResolveRevision(commit) = %q %q %v", full, oid, err)
			}
			// A tag's own ID is not a commit ID, even though it points to one.
			for _, requested := range []string{r.missing, r.tag, r.tree} {
				if _, _, err := m.ResolveRevision(ctx, r.id, requested); !errors.Is(err, ErrNotFound) {
					t.Fatalf("ResolveRevision(%s) = %v, want ErrNotFound", requested, err)
				}
			}
			for _, oid := range []string{r.missing, r.tag, r.tree, "not-an-id"} {
				if _, _, err := m.CommitFiles(ctx, r.id, oid); !errors.Is(err, ErrNotFound) {
					t.Fatalf("CommitFiles(%s) = %v, want ErrNotFound", oid, err)
				}
			}
			if _, err := m.CommitReachableFrom(ctx, r.id, r.commit, r.missing); !errors.Is(err, ErrNotFound) {
				t.Fatalf("CommitReachableFrom(missing) = %v, want ErrNotFound", err)
			}
			for _, filePath := range []string{"nope", "file.txt/deeper", "../escape"} {
				if _, err := m.PathAt(ctx, r.id, r.commit, filePath); !errors.Is(err, ErrNotFound) {
					t.Fatalf("PathAt(%q) = %v, want ErrNotFound", filePath, err)
				}
			}
			if _, err := m.TreeAt(ctx, r.id, r.commit, "nope"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("TreeAt(nope) = %v, want ErrNotFound", err)
			}

			// Absence is asked again every time, while a found commit is
			// answered from the cache, and a commit that reads normally
			// costs one Git process.
			for range 2 {
				before := count()
				if _, _, err := m.ResolveRevision(ctx, r.id, r.missing); !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
				if started := count() - before; started != 1 {
					t.Fatalf("an absent commit started %d Git processes, want 1", started)
				}
			}
			before := count()
			if _, _, err := m.ResolveRevision(ctx, r.id, r.commit); err != nil {
				t.Fatal(err)
			}
			if started := count() - before; started != 0 {
				t.Fatalf("a found commit started %d Git processes, want 0 from the cache", started)
			}
			before = count()
			if _, _, err := m.CommitFiles(ctx, r.id, r.commit); err != nil {
				t.Fatal(err)
			}
			// The file records, then the line counts, then the object metadata that
			// says whether the change is above the memory line: the records name
			// every changed file without reading content, the metadata is read
			// once the files are known, and the counts come last so the files
			// above the line are left out of them (see commitFileCounts).
			if started := count() - before; started != 3 {
				t.Fatalf("reading an existing commit started %d Git processes, want 3", started)
			}
		})
	}
}

// malformedGit replaces the manager's runner with one that prints output and
// succeeds for any process with an argument command, as a Git that answered
// something unexpected would.
func malformedGit(t *testing.T, manager *Manager, command, output string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "git")
	script := "#!/bin/sh\nfor a in \"$@\"; do if [ \"$a\" = " + shellQuote(command) + " ]; then cat >/dev/null; echo " + shellQuote(output) + "; exit 0; fi; done\n" +
		"exec " + shellQuote(gitPath) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	runner, err := gitexec.New(wrapper, filepath.Join(dir, "runtime"))
	noErr(t, err)
	manager.Git = runner
}

// A restore source that Git could not look up is a failure, never an
// invalid request. An existing commit and an object that is not a commit
// keep their answers.
func TestRestoreSourceLookupFailureIsNotInvalid(t *testing.T) {
	requirePOSIX(t)
	r := newReadOutcomeRepository(t, ObjectFormatSHA1)
	ctx := context.Background()
	request := func(source string) RestoreRequest {
		return RestoreRequest{Source: source, Target: "main", Mode: RestoreAll}
	}
	_, failPath, _ := countGitProcesses(t, r.manager, "cat-file")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	if _, err := r.manager.PreviewRestore(ctx, r.id, request(r.parent)); err == nil || errors.Is(err, ErrRestoreInvalid) {
		t.Fatalf("PreviewRestore with a failing lookup = %v, want a failure that is not ErrRestoreInvalid", err)
	}
	noErr(t, os.Remove(failPath))
	if _, err := r.manager.PreviewRestore(ctx, r.id, request(r.parent)); err != nil {
		t.Fatalf("PreviewRestore(existing commit) = %v", err)
	}
	for _, source := range []string{r.missing, r.tag, r.tree} {
		if _, err := r.manager.PreviewRestore(ctx, r.id, request(source)); !errors.Is(err, ErrRestoreInvalid) {
			t.Fatalf("PreviewRestore(%s) = %v, want ErrRestoreInvalid", source, err)
		}
	}
}

// A file above what one Git process may use is refused with an explicit
// too-large state and without running Git, which would rebuild a large stored
// delta in memory; below that bound, or with an unknown memory ceiling, the
// read is as before and shows the prefix within the display limit. The
// ceiling is forced so the refusal runs on every platform.
func TestBlobAboveTheMemoryBoundIsRefusedWithoutRunningGit(t *testing.T) {
	requirePOSIX(t)
	saved := hostmem.Ceiling
	hostmem.Ceiling = func() uint64 { return 512 << 20 }
	defer func() { hostmem.Ceiling = saved }()

	manager, _, work := newTestRepository(t)
	commit := commitTree(t, manager, work, "main", map[string]string{"f.txt": strings.Repeat("x", 3000)})
	entries, err := manager.TreeAt(context.Background(), "sample", commit, "")
	noErr(t, err)
	entry := entries[0]
	bound := manager.Git.ReadBound()
	if bound == 0 {
		t.Fatal("the forced ceiling gave no read bound")
	}
	prefix, err := manager.BlobAt(context.Background(), "sample", entry, 1000)
	if err != nil || len(prefix.Content) != 1000 || !prefix.Truncated || prefix.TooLarge {
		t.Fatalf("prefix view = %d bytes truncated=%v tooLarge=%v err=%v; want 1000 bytes, truncated",
			len(prefix.Content), prefix.Truncated, prefix.TooLarge, err)
	}
	count, _, _ := countGitProcesses(t, manager, "*")
	before := count()
	large := entry
	large.Size = bound + 1
	blob, err := manager.BlobAt(context.Background(), "sample", large, 1000)
	if err != nil || !blob.TooLarge || blob.Truncated || len(blob.Content) != 0 || count() != before {
		t.Fatalf("blob = %d bytes truncated=%v tooLarge=%v err=%v, git runs %d; want too large, empty, no Git",
			len(blob.Content), blob.Truncated, blob.TooLarge, err, count()-before)
	}
	// An unknown ceiling cannot be compared, so the large listed size is read
	// and cut at the display limit as before.
	hostmem.Ceiling = func() uint64 { return 0 }
	blob, err = manager.BlobAt(context.Background(), "sample", large, 1000)
	if err != nil || blob.TooLarge || !blob.Truncated || len(blob.Content) != 1000 {
		t.Fatalf("unknown ceiling: %d bytes truncated=%v tooLarge=%v err=%v; want the 1000 byte prefix",
			len(blob.Content), blob.Truncated, blob.TooLarge, err)
	}
	// The pinned reads share the rule: a listed size above the bound is
	// refused, and no blob is read. The wrapper fails every "cat-file blob"
	// argument, so a read that reached Git would report that failure instead.
	hostmem.Ceiling = func() uint64 { return 512 << 20 }
	big := commitTree(t, manager, work, "main", map[string]string{"big.txt": strings.Repeat("y", int(bound)+1)})
	view, err := manager.PathAt(context.Background(), "sample", big, "big.txt")
	noErr(t, err)
	pinned, err := manager.PinRepository(context.Background(), "sample", big, big)
	noErr(t, err)
	_, failPath, _ := countGitProcesses(t, manager, "blob")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	if _, err := pinned.ReadBlob(context.Background(), PinnedHead, "big.txt", 0, 1<<20, 1<<20, 1<<20); !errors.Is(err, ErrPinnedBlobTooLarge) {
		t.Fatalf("pinned chunk read = %v; want the bound refusal before cat-file", err)
	}
	if _, err := pinned.ReadBlobObject(context.Background(), view.File.OID, view.File.Size); !errors.Is(err, ErrPinnedBlobTooLarge) {
		t.Fatalf("pinned object read = %v; want the bound refusal before cat-file", err)
	}
}
