package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// commitTree commits files in work, pushes the commit to branch and records
// an OwnGit write, and returns the commit.
func commitTree(t *testing.T, manager *Manager, work, branch string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(work, filepath.FromSlash(name))
		noErr(t, os.MkdirAll(filepath.Dir(full), 0o700))
		noErr(t, os.WriteFile(full, []byte(content), 0o600))
	}
	runGit(t, work, "add", "-A")
	runGit(t, work, "commit", "-q", "-m", "commit "+branch)
	runGit(t, work, "push", "-q", "--force", "origin", "HEAD:refs/heads/"+branch)
	wroteRefs(manager, "sample")
	return gitOutput(t, work, "rev-parse", "HEAD")
}

// setForTest changes a package bound until the test ends.
func setForTest[T any](t *testing.T, target *T, value T) {
	t.Helper()
	previous := *target
	*target = value
	t.Cleanup(func() { *target = previous })
}

func requirePOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a POSIX-shell fixture")
	}
}

// A branch and a lightweight tag resolve from the ref snapshot without Git.
// An annotated tag, also one of another tag, is peeled to its commit with
// Git once and then answered from the cache; a tag of a tree or a blob names
// no commit.
func TestResolveRefPeelsAnnotatedAndNestedTags(t *testing.T) {
	requirePOSIX(t)
	manager, _, work := newTestRepository(t)
	commit := commitTree(t, manager, work, "main", map[string]string{"file.txt": "one\n"})
	runGit(t, work, "tag", "light")
	runGit(t, work, "tag", "-a", "-m", "annotated", "annotated")
	runGit(t, work, "tag", "-a", "-m", "nested", "nested", "annotated")
	runGit(t, work, "tag", "-a", "-m", "of a tree", "tree-tag", "HEAD^{tree}")
	runGit(t, work, "tag", "-a", "-m", "of a blob", "blob-tag", "HEAD:file.txt")
	runGit(t, work, "push", "-q", "origin", "--tags")
	wroteRefs(manager, "sample")
	mustSnapshot(t, manager)
	count, _, _ := countGitProcesses(t, manager, "*")

	for _, check := range []struct {
		ref, full string
		processes int
	}{
		{"", "refs/heads/main", 0},
		{"main", "refs/heads/main", 0},
		{"light", "refs/tags/light", 0},
		{"refs/tags/annotated", "refs/tags/annotated", 1},
		{"annotated", "refs/tags/annotated", 0},
		{"nested", "refs/tags/nested", 1},
		{"nested", "refs/tags/nested", 0},
	} {
		before := count()
		full, oid, err := manager.ResolveRef(context.Background(), "sample", check.ref)
		if err != nil || full != check.full || oid != commit {
			t.Fatalf("ResolveRef(%q) = %q %q %v, want %q %s", check.ref, full, oid, err, check.full, commit)
		}
		if started := count() - before; started != check.processes {
			t.Fatalf("ResolveRef(%q) started %d Git processes, want %d", check.ref, started, check.processes)
		}
	}
	for _, ref := range []string{"tree-tag", "refs/tags/blob-tag", "missing"} {
		if full, oid, err := manager.ResolveRef(context.Background(), "sample", ref); err == nil {
			t.Fatalf("ResolveRef(%q) = %q %q, want an error", ref, full, oid)
		}
	}
}

// Folder listings are cached by commit and path, so two folders with the
// same content keep their own paths.
func TestCachedListingsKeepTheirPaths(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commit := commitTree(t, manager, work, "main", map[string]string{"a/x.txt": "same\n", "b/x.txt": "same\n"})
	if gitOutput(t, work, "rev-parse", "HEAD:a") != gitOutput(t, work, "rev-parse", "HEAD:b") {
		t.Fatal("the fixture folders differ")
	}
	for round := 0; round < 2; round++ {
		for _, folder := range []string{"a", "b"} {
			entries, err := manager.TreeAt(context.Background(), "sample", commit, folder)
			if err != nil || len(entries) != 1 || entries[0].Name != "x.txt" || entries[0].Path != folder+"/x.txt" {
				t.Fatalf("TreeAt(%s) = %+v %v", folder, entries, err)
			}
			blob, siblings, err := manager.FileAt(context.Background(), "sample", commit, folder+"/x.txt", 1024)
			if err != nil || string(blob.Content) != "same\n" || blob.Path != folder+"/x.txt" || len(siblings) != 1 || siblings[0].Path != folder+"/x.txt" {
				t.Fatalf("FileAt(%s/x.txt) = %+v %+v %v", folder, blob, siblings, err)
			}
		}
	}
	view, err := manager.PathAt(context.Background(), "sample", commit, "a")
	if err != nil || !view.Folder || len(view.Entries) != 1 || view.Entries[0].Path != "a/x.txt" {
		t.Fatalf("PathAt(a) = %+v %v", view, err)
	}
	for _, missing := range []string{"c", "a/x.txt/y", "a/y.txt"} {
		if view, err := manager.PathAt(context.Background(), "sample", commit, missing); err == nil {
			t.Fatalf("PathAt(%s) = %+v, want an error", missing, view)
		}
	}
}

// The cache keeps at most its entry count and bytes, evicting the least
// recently used entry, and never keeps a file larger than one entry.
func TestObjectCacheEvictsAndSkipsLargeFiles(t *testing.T) {
	requirePOSIX(t)
	manager, _, work := newTestRepository(t)
	files := map[string]string{"big.txt": strings.Repeat("b", 3000)}
	for index := 0; index < 5; index++ {
		files[fmt.Sprintf("f%d.txt", index)] = strings.Repeat(fmt.Sprint(index), 500)
	}
	commit := commitTree(t, manager, work, "main", files)
	entries, err := manager.TreeAt(context.Background(), "sample", commit, "")
	noErr(t, err)
	byName := map[string]TreeEntry{}
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	setForTest(t, &objectCacheEntries, 3)
	setForTest(t, &objectCacheBytes, 2000)
	setForTest(t, &objectCacheItem, 1000)
	count, _, _ := countGitProcesses(t, manager, "*")
	read := func(name string) int {
		t.Helper()
		before := count()
		blob, err := manager.BlobAt(context.Background(), "sample", byName[name], 1<<20)
		if err != nil || string(blob.Content) != files[name] || blob.Truncated {
			t.Fatalf("BlobAt(%s) = %d bytes truncated=%v err=%v", name, len(blob.Content), blob.Truncated, err)
		}
		return count() - before
	}
	for index := 0; index < 5; index++ {
		read(fmt.Sprintf("f%d.txt", index))
		entries, bytes := manager.objects.usage()
		if entries > 3 || bytes > 2000 {
			t.Fatalf("the cache holds %d entries and %d bytes", entries, bytes)
		}
	}
	if started := read("f4.txt"); started != 0 {
		t.Fatalf("the newest entry started %d Git processes", started)
	}
	if started := read("f0.txt"); started != 1 {
		t.Fatalf("an evicted entry started %d Git processes, want 1", started)
	}
	if read("big.txt") != 1 || read("big.txt") != 1 {
		t.Fatal("a file larger than one entry was cached")
	}
}

// A caller changing a cached result changes neither the cache nor another
// caller's copy.
func TestObjectCacheReturnsCopies(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commit := commitTree(t, manager, work, "main", map[string]string{"file.txt": "original\n"})
	first, _, err := manager.FileAt(context.Background(), "sample", commit, "file.txt", 1024)
	noErr(t, err)
	copy(first.Content, "CHANGED!")
	second, entries, err := manager.FileAt(context.Background(), "sample", commit, "file.txt", 1024)
	noErr(t, err)
	if string(second.Content) != "original\n" {
		t.Fatalf("a caller's change reached the cache: %q", second.Content)
	}
	entries[0].Name = "changed"
	if again, err := manager.TreeAt(context.Background(), "sample", commit, ""); err != nil || again[0].Name != "file.txt" {
		t.Fatalf("a caller's change reached the listing: %+v %v", again, err)
	}
}

// Concurrent misses for one result start one Git process. A waiting caller
// whose request ends stops waiting at once.
func TestObjectCacheRunsConcurrentMissesOnce(t *testing.T) {
	requirePOSIX(t)
	manager, _, work := newTestRepository(t)
	commit := commitTree(t, manager, work, "main", map[string]string{"file.txt": "shared\n"})
	entries, err := manager.TreeAt(context.Background(), "sample", commit, "")
	noErr(t, err)
	count, _, slowPath := countGitProcesses(t, manager, "cat-file")
	noErr(t, os.WriteFile(slowPath, nil, 0o600))
	before := count()
	var group sync.WaitGroup
	results := make([]string, 8)
	errs := make([]error, 8)
	for index := range results {
		group.Add(1)
		go func() {
			defer group.Done()
			blob, err := manager.BlobAt(context.Background(), "sample", entries[0], 1024)
			results[index], errs[index] = string(blob.Content), err
		}()
	}
	// Once the shared read runs, a caller with a short deadline joins it.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(slowPath + ".started"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the shared read never started")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := manager.BlobAt(ctx, "sample", entries[0], 1024); !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("a waiting caller whose request ended got %v after %s", err, time.Since(started))
	}
	group.Wait()
	for index := range results {
		if errs[index] != nil || results[index] != "shared\n" {
			t.Fatalf("caller %d got %q %v", index, results[index], errs[index])
		}
	}
	if started := count() - before; started != 1 {
		t.Fatalf("8 concurrent misses started %d Git processes, want 1", started)
	}
}

// A failed read is reported and not cached.
func TestObjectCacheDoesNotCacheErrors(t *testing.T) {
	requirePOSIX(t)
	manager, _, work := newTestRepository(t)
	commit := commitTree(t, manager, work, "main", map[string]string{"file.txt": "content\n"})
	count, failPath, _ := countGitProcesses(t, manager, "ls-tree")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	if _, err := manager.TreeAt(context.Background(), "sample", commit, ""); err == nil {
		t.Fatal("a failed listing reported no error")
	}
	noErr(t, os.Remove(failPath))
	before := count()
	if entries, err := manager.TreeAt(context.Background(), "sample", commit, ""); err != nil || len(entries) != 1 || count() == before {
		t.Fatalf("after the failure: entries=%+v err=%v processes=%d", entries, err, count()-before)
	}
}

// A deleted repository's cached results never answer for a new repository
// with the same name, even for the same object IDs.
func TestObjectCacheForgetsADeletedRepository(t *testing.T) {
	requirePOSIX(t)
	manager, remote, work := newTestRepository(t)
	old := commitTree(t, manager, work, "main", map[string]string{"file.txt": "old repository\n"})
	if blob, _, err := manager.FileAt(context.Background(), "sample", old, "file.txt", 1024); err != nil || string(blob.Content) != "old repository\n" {
		t.Fatalf("FileAt before deletion = %q %v", blob.Content, err)
	}
	if _, err := manager.Delete(context.Background(), "sample", DeleteFiles); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background(), "sample", "same name"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.FileAt(context.Background(), "sample", old, "file.txt", 1024); err == nil {
		t.Fatal("the new repository answered with the deleted repository's file")
	}
	count, _, _ := countGitProcesses(t, manager, "*")
	runGit(t, work, "push", "-q", "--force", "origin", "HEAD:refs/heads/main")
	wroteRefs(manager, "sample")
	if gitOutput(t, "", "--git-dir", remote, "rev-parse", "main") != old {
		t.Fatal("the fixture did not push the same commit again")
	}
	before := count()
	if blob, _, err := manager.FileAt(context.Background(), "sample", old, "file.txt", 1024); err != nil || string(blob.Content) != "old repository\n" || count() == before {
		t.Fatalf("FileAt after pushing the commit again = %q %v, processes=%d", blob.Content, err, count()-before)
	}
}

// Compare counts changes from the merge base, stops a diff at its output
// limit and caches it with that limit, and shows a diff stopped by its time
// limit as incomplete without caching it.
func TestCompareLimits(t *testing.T) {
	requirePOSIX(t)
	manager, _, work := newTestRepository(t)
	base := commitTree(t, manager, work, "main", map[string]string{"shared.txt": "shared\n"})
	runGit(t, work, "checkout", "-q", "-b", "feature")
	files := map[string]string{}
	for index := 0; index < 20; index++ {
		files[fmt.Sprintf("f%02d.txt", index)] = strings.Repeat(fmt.Sprintf("line %d\n", index), 50)
	}
	source := commitTree(t, manager, work, "feature", files)
	runGit(t, work, "checkout", "-q", "main")
	target := commitTree(t, manager, work, "main", map[string]string{"shared.txt": "moved\n", "main.txt": "main\n"})

	full, err := manager.Compare(context.Background(), "sample", target, source)
	noErr(t, err)
	if full.Bases != 1 || full.Base != base || len(full.Files) != 20 || full.PatchTruncated || full.FilesTruncated || strings.Count(full.Patch, "diff --git ") != 20 {
		t.Fatalf("full comparison: bases=%d files=%d cut=%v/%v", full.Bases, len(full.Files), full.PatchTruncated, full.FilesTruncated)
	}
	count, _, slowPath := countGitProcesses(t, manager, "diff")

	// A limit inside the patches keeps the whole file list.
	setForTest(t, &compareOutputLimit, int64(len(full.Patch)/2))
	before := count()
	cut, err := manager.Compare(context.Background(), "sample", target, source)
	noErr(t, err)
	if !cut.PatchTruncated || cut.FilesTruncated || len(cut.Files) != 20 || len(cut.Patch) >= len(full.Patch) || count()-before != 1 {
		t.Fatalf("patch cut: files=%d cut=%v/%v patch=%d processes=%d", len(cut.Files), cut.PatchTruncated, cut.FilesTruncated, len(cut.Patch), count()-before)
	}
	before = count()
	if again, err := manager.Compare(context.Background(), "sample", target, source); err != nil || !again.PatchTruncated || count() != before {
		t.Fatalf("a comparison cut by its output limit was not cached: %v processes=%d", err, count()-before)
	}

	// A limit inside the file records keeps only the files read in full.
	setForTest(t, &compareOutputLimit, 300)
	short, err := manager.Compare(context.Background(), "sample", target, source)
	noErr(t, err)
	if !short.FilesTruncated || len(short.Files) >= 20 || short.Patch != "" {
		t.Fatalf("list cut: files=%d cut=%v", len(short.Files), short.FilesTruncated)
	}

	// The time limit shows what was read as incomplete and caches nothing.
	setForTest(t, &compareOutputLimit, 8<<20-1)
	setForTest(t, &compareTimeLimit, 300*time.Millisecond)
	noErr(t, os.WriteFile(slowPath, nil, 0o600))
	for attempt := 0; attempt < 2; attempt++ {
		before = count()
		slow, err := manager.Compare(context.Background(), "sample", target, source)
		if err != nil || !slow.FilesTruncated || !slow.PatchTruncated || count()-before != 1 {
			t.Fatalf("attempt %d past the time limit: %+v %v processes=%d", attempt, slow, err, count()-before)
		}
	}
}
