package checksource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

// These tests build synthetic repositories with Git plumbing in a temporary
// directory. They never read a real user repository.

func newSyntheticRepository(t *testing.T) (*repository.Manager, string) {
	t.Helper()
	root := t.TempDir()
	store, err := state.Open(context.Background(), filepath.Join(root, "state"))
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	runner, err := gitexec.New("", filepath.Join(root, "runtime"))
	noErr(t, err)
	repositories := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositories, 0o700))
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositories}
	if _, err := manager.Create(context.Background(), "sample", "synthetic check source"); err != nil {
		t.Fatal(err)
	}
	bare, err := manager.Path("sample")
	noErr(t, err)
	return manager, bare
}

// git runs one plumbing command against an explicit --git-dir. It runs from a
// scratch directory so no ambient repository can influence the fixture.
func git(t *testing.T, stdin []byte, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = t.TempDir()
	command.Env = testfixture.GitEnvironment(append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Check Source", "GIT_AUTHOR_EMAIL=checksource@example.invalid",
		"GIT_COMMITTER_NAME=Check Source", "GIT_COMMITTER_EMAIL=checksource@example.invalid",
		"GIT_AUTHOR_DATE=2024-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2024-01-01T00:00:00Z",
	))
	if stdin != nil {
		command.Stdin = strings.NewReader(string(stdin))
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// treeEntry is one entry as Git records it in a tree object.
type treeEntry struct {
	mode       string
	objectType string
	oid        string
}

// treeBuilder assembles nested trees with plumbing only, so no working tree,
// index, or checkout participates in the fixture.
type treeBuilder struct {
	bare    string
	entries map[string]treeEntry
}

func newTreeBuilder(bare string) *treeBuilder {
	return &treeBuilder{bare: bare, entries: map[string]treeEntry{}}
}

func (b *treeBuilder) blob(t *testing.T, name, mode string, content []byte) {
	t.Helper()
	oid := git(t, content, "--git-dir", b.bare, "hash-object", "-w", "--stdin")
	b.entries[name] = treeEntry{mode: mode, objectType: "blob", oid: oid}
}

func (b *treeBuilder) gitlink(t *testing.T, name, commitOID string) {
	t.Helper()
	b.entries[name] = treeEntry{mode: "160000", objectType: "commit", oid: commitOID}
}

// commit writes the collected paths as nested trees and returns the commit ID.
func (b *treeBuilder) commit(t *testing.T) string {
	t.Helper()
	tree := b.writeTree(t, "")
	return git(t, []byte("synthetic check source\n"), "--git-dir", b.bare, "commit-tree", tree)
}

func (b *treeBuilder) writeTree(t *testing.T, prefix string) string {
	t.Helper()
	direct := map[string]treeEntry{}
	subdirectories := map[string]struct{}{}
	for path, entry := range b.entries {
		if prefix != "" {
			if !strings.HasPrefix(path, prefix+"/") {
				continue
			}
			path = path[len(prefix)+1:]
		}
		if separator := strings.IndexByte(path, '/'); separator >= 0 {
			subdirectories[path[:separator]] = struct{}{}
			continue
		}
		direct[path] = entry
	}
	for name := range subdirectories {
		childPrefix := name
		if prefix != "" {
			childPrefix = prefix + "/" + name
		}
		direct[name] = treeEntry{mode: "040000", objectType: "tree", oid: b.writeTree(t, childPrefix)}
	}
	var input strings.Builder
	for name, entry := range direct {
		input.WriteString(entry.mode + " " + entry.objectType + " " + entry.oid + "\t" + name)
		input.WriteByte(0)
	}
	return git(t, []byte(input.String()), "--git-dir", b.bare, "mktree", "-z")
}

func pin(t *testing.T, manager *repository.Manager, commitOID string) *repository.PinnedRepository {
	t.Helper()
	pinned, err := manager.PinRepository(context.Background(), "sample", commitOID, commitOID)
	noErr(t, err)
	return pinned
}

func TestPinnedMaterializationKeepsExactBytesUnderCommittedAttributes(t *testing.T) {
	manager, bare := newSyntheticRepository(t)
	builder := newTreeBuilder(bare)
	// These attributes make git checkout-index rewrite line endings and drop
	// the ignored file. Raw object access must not apply either rule.
	builder.blob(t, ".gitattributes", "100644", []byte(
		"*.txt text eol=crlf\n*.bin binary\nsecret.txt export-ignore\nfiltered.txt filter=definitely-missing\n"))
	builder.blob(t, "text/lf.txt", "100644", []byte("one\ntwo\n"))
	builder.blob(t, "filtered.txt", "100644", []byte("kept\nbytes\n"))
	builder.blob(t, "secret.txt", "100644", []byte("not exported by archive\n"))
	builder.blob(t, "assets/image.bin", "100644", []byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0x0d, 0x0a, 0x1a, 0x0a})
	builder.blob(t, "scripts/build.sh", "100755", []byte("#!/bin/sh\nexit 0\n"))
	commitOID := builder.commit(t)

	destination := filepath.Join(t.TempDir(), "source")
	result, err := MaterializePinned(context.Background(), pin(t, manager, commitOID), repository.PinnedHead, destination, Options{})
	noErr(t, err)
	if result.CommitOID != commitOID || result.ObjectFormat == "" {
		t.Fatalf("unexpected source identity: %+v", result)
	}
	for path, want := range map[string]string{
		"text/lf.txt":  "one\ntwo\n",
		"filtered.txt": "kept\nbytes\n",
		"secret.txt":   "not exported by archive\n",
	} {
		content, readErr := os.ReadFile(filepath.Join(destination, filepath.FromSlash(path)))
		if readErr != nil {
			t.Fatalf("%s was omitted or unreadable: %v", path, readErr)
		}
		if string(content) != want {
			t.Fatalf("%s bytes changed under committed attributes: %q", path, content)
		}
	}
	binary, err := os.ReadFile(filepath.Join(destination, "assets", "image.bin"))
	noErr(t, err)
	if len(binary) != 9 || binary[0] != 0x89 || binary[5] != 0x0d {
		t.Fatalf("binary bytes changed: %v", binary)
	}

	// Every manifest OID must match what Git itself records for the commit.
	listing := git(t, nil, "--git-dir", bare, "ls-tree", "-r", commitOID)
	recorded := map[string]string{}
	for _, line := range strings.Split(listing, "\n") {
		metadata, name, found := strings.Cut(line, "\t")
		if !found {
			t.Fatalf("unexpected ls-tree line %q", line)
		}
		fields := strings.Fields(metadata)
		recorded[name] = fields[0] + " " + fields[2]
	}
	if len(recorded) != len(result.Files) {
		t.Fatalf("manifest has %d files, Git lists %d", len(result.Files), len(recorded))
	}
	for _, file := range result.Files {
		if recorded[file.Path] != file.Mode+" "+file.OID {
			t.Fatalf("manifest identity for %q is %s %s, Git records %q", file.Path, file.Mode, file.OID, recorded[file.Path])
		}
	}
	for _, file := range result.Files {
		if file.Path != "scripts/build.sh" {
			continue
		}
		if !file.Executable || file.Mode != ModeExecutable {
			t.Fatalf("executable mode was not preserved: %+v", file)
		}
		info, statErr := os.Stat(filepath.Join(destination, "scripts", "build.sh"))
		noErr(t, statErr)
		if file.ExecutableApplied != (info.Mode()&0o100 != 0) {
			t.Fatalf("reported execute bit %v disagrees with mode %v", file.ExecutableApplied, info.Mode())
		}
	}
}

func TestPinnedMaterializationRefusesCommittedSymlinksAndSubmodules(t *testing.T) {
	manager, bare := newSyntheticRepository(t)

	linked := newTreeBuilder(bare)
	linked.blob(t, "keep.txt", "100644", []byte("keep\n"))
	// A committed link chain: the first target looks internal and the second
	// escapes through it, which lexical validation alone would accept.
	linked.blob(t, "inside/link", "120000", []byte(".."))
	linked.blob(t, "escape", "120000", []byte("inside/link/../../outside-sentinel"))
	linkedCommit := linked.commit(t)

	destination := filepath.Join(t.TempDir(), "linked")
	result, err := MaterializePinned(context.Background(), pin(t, manager, linkedCommit), repository.PinnedHead, destination, Options{})
	if result != nil || !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("result=%+v err=%v, want unsupported entry", result, err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused link tree created the destination: %v", statErr)
	}

	cycle := newTreeBuilder(bare)
	cycle.blob(t, "a", "120000", []byte("b"))
	cycle.blob(t, "b", "120000", []byte("a"))
	cycleCommit := cycle.commit(t)
	if _, err := MaterializePinned(context.Background(), pin(t, manager, cycleCommit), repository.PinnedHead,
		filepath.Join(t.TempDir(), "cycle"), Options{}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("symlink cycle err=%v, want unsupported entry", err)
	}

	module := newTreeBuilder(bare)
	module.blob(t, "keep.txt", "100644", []byte("keep\n"))
	module.gitlink(t, "vendor/module", linkedCommit)
	moduleCommit := module.commit(t)
	if _, err := MaterializePinned(context.Background(), pin(t, manager, moduleCommit), repository.PinnedHead,
		filepath.Join(t.TempDir(), "module"), Options{}); !errors.Is(err, ErrUnsupportedEntry) {
		t.Fatalf("gitlink err=%v, want unsupported entry", err)
	}
}

func TestPinnedMaterializationRefusesGitControlAliases(t *testing.T) {
	manager, bare := newSyntheticRepository(t)
	builder := newTreeBuilder(bare)
	builder.blob(t, "keep.txt", "100644", []byte("keep\n"))
	builder.blob(t, ".git/config", "100644", []byte("[core]\n\thooksPath = /tmp/hooks\n"))
	commitOID := builder.commit(t)

	destination := filepath.Join(t.TempDir(), "source")
	result, err := MaterializePinned(context.Background(), pin(t, manager, commitOID), repository.PinnedHead, destination, Options{})
	if result != nil || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("result=%+v err=%v, want unsafe path", result, err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused control path created the destination: %v", statErr)
	}
}

func TestPinnedMaterializationEnforcesBoundsBeforeWriting(t *testing.T) {
	manager, bare := newSyntheticRepository(t)
	builder := newTreeBuilder(bare)
	builder.blob(t, "a.txt", "100644", []byte("aaaa\n"))
	builder.blob(t, "b.txt", "100644", []byte("bbbb\n"))
	commitOID := builder.commit(t)
	pinned := pin(t, manager, commitOID)

	destination := filepath.Join(t.TempDir(), "bounded")
	if _, err := MaterializePinned(context.Background(), pinned, repository.PinnedHead, destination,
		Options{Limits: Limits{MaxTotalBytes: 6}}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("err=%v, want limit exceeded", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a bounded refusal created the destination: %v", statErr)
	}

	// A metadata limit smaller than the listing must fail rather than
	// materialize a silently shortened tree.
	if _, err := MaterializePinned(context.Background(), pinned, repository.PinnedHead,
		filepath.Join(t.TempDir(), "metadata"), Options{Limits: Limits{MetadataLimit: 8}}); err == nil {
		t.Fatal("a truncated tree listing was accepted")
	}
}

func TestPinnedMaterializationPreservesLFSPointers(t *testing.T) {
	manager, bare := newSyntheticRepository(t)
	pointer := []byte("version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\nsize 12\n")
	builder := newTreeBuilder(bare)
	builder.blob(t, ".gitattributes", "100644", []byte("*.psd filter=lfs diff=lfs merge=lfs -text\n"))
	builder.blob(t, "design/logo.psd", "100644", pointer)
	commitOID := builder.commit(t)

	destination := filepath.Join(t.TempDir(), "source")
	result, err := MaterializePinned(context.Background(), pin(t, manager, commitOID), repository.PinnedHead, destination, Options{})
	noErr(t, err)
	if len(result.LFSPointerPaths) != 1 || result.LFSPointerPaths[0] != "design/logo.psd" {
		t.Fatalf("pointer detection reported %v", result.LFSPointerPaths)
	}
	content, err := os.ReadFile(filepath.Join(destination, "design", "logo.psd"))
	noErr(t, err)
	if string(content) != string(pointer) {
		t.Fatalf("pointer bytes were resolved or changed: %q", content)
	}
}

func TestPinnedMaterializationLeavesAnExistingDestinationUnchanged(t *testing.T) {
	manager, bare := newSyntheticRepository(t)
	builder := newTreeBuilder(bare)
	builder.blob(t, "a.txt", "100644", []byte("materialized\n"))
	commitOID := builder.commit(t)

	root := t.TempDir()
	destination := filepath.Join(root, "existing")
	noErr(t, os.Mkdir(destination, 0o700))
	sentinel := filepath.Join(destination, "a.txt")
	noErr(t, os.WriteFile(sentinel, []byte("sentinel\n"), 0o600))
	if _, err := MaterializePinned(context.Background(), pin(t, manager, commitOID), repository.PinnedHead,
		destination, Options{}); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("err=%v, want existing destination", err)
	}
	content, err := os.ReadFile(sentinel)
	if err != nil || string(content) != "sentinel\n" {
		t.Fatalf("existing destination changed: %q err=%v", content, err)
	}
}

func TestPinnedSourceRejectsAnInvalidSide(t *testing.T) {
	manager, bare := newSyntheticRepository(t)
	builder := newTreeBuilder(bare)
	builder.blob(t, "a.txt", "100644", []byte("a\n"))
	pinned := pin(t, manager, builder.commit(t))
	if _, err := NewPinnedSource(pinned, repository.PinnedSide(0)); err == nil {
		t.Fatal("an invalid pinned side was accepted")
	}
	if _, err := NewPinnedSource(nil, repository.PinnedHead); err == nil {
		t.Fatal("a missing pinned repository was accepted")
	}
}

// Waiting for a busy repository ends when the caller's context ends or the
// bound elapses, and stops at the first answer that is not busy.
func TestRetryWhileRepositoryBusyIsBounded(t *testing.T) {
	calls := 0
	err := RetryWhileRepositoryBusy(context.Background(), time.Minute, func() error {
		calls++
		if calls < 3 {
			return fmt.Errorf("read: %w", repository.ErrPinnedRepositoryBusy)
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("retry until free: calls=%d err=%v", calls, err)
	}

	other := errors.New("not busy")
	calls = 0
	if err := RetryWhileRepositoryBusy(context.Background(), time.Minute, func() error { calls++; return other }); err != other || calls != 1 {
		t.Fatalf("another failure was retried: calls=%d err=%v", calls, err)
	}

	busy := func() error { return repository.ErrPinnedRepositoryBusy }
	started := time.Now()
	if err := RetryWhileRepositoryBusy(context.Background(), 100*time.Millisecond, busy); !errors.Is(err, repository.ErrPinnedRepositoryBusy) || time.Since(started) > 5*time.Second {
		t.Fatalf("bound: err=%v after %s", err, time.Since(started))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started = time.Now()
	if err := RetryWhileRepositoryBusy(ctx, time.Hour, busy); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 5*time.Second {
		t.Fatalf("cancellation: err=%v after %s", err, time.Since(started))
	}
}
