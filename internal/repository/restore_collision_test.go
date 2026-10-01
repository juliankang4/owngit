package repository

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func restoreCollisionCommits(t *testing.T, remote, prefix, child string) (string, string) {
	t.Helper()
	object := func(input string, arguments ...string) string {
		return gitInputOutput(t, remote, []byte(input), append([]string{"--git-dir", "."}, arguments...)...)
	}
	blob := object("synthetic data\n", "hash-object", "-w", "--stdin")
	fileTree := object("100644 blob "+blob+"\tnode\n100644 blob "+blob+"\tnode-neighbor\n100644 blob "+blob+"\tnode.txt\n", "mktree")
	components := strings.Split(child, "/")
	directoryTree := object("100644 blob "+blob+"\t"+components[len(components)-1]+"\n", "mktree")
	for index := len(components) - 2; index >= 0; index-- {
		directoryTree = object("040000 tree "+directoryTree+"\t"+components[index]+"\n", "mktree")
	}
	directoryTree = object("040000 tree "+directoryTree+"\tnode\n100644 blob "+blob+"\tnode-neighbor\n100644 blob "+blob+"\tnode.txt\n", "mktree")
	if prefix != "" {
		components := strings.Split(strings.TrimSuffix(prefix, "/"), "/")
		for index := len(components) - 1; index >= 0; index-- {
			fileTree = object("040000 tree "+fileTree+"\t"+components[index]+"\n", "mktree")
			directoryTree = object("040000 tree "+directoryTree+"\t"+components[index]+"\n", "mktree")
		}
	}
	commitArguments := []string{"-c", "user.name=Restore Fixture", "-c", "user.email=restore@example.invalid", "commit-tree"}
	fileCommit := object("file tree\n", append(commitArguments, fileTree)...)
	directoryCommit := object("directory tree\n", append(commitArguments, directoryTree, "-p", fileCommit)...)
	return fileCommit, directoryCommit
}

func duplicateFlattenedPathCommits(t *testing.T, work string) (valid, malformed string) {
	t.Helper()
	object := func(input []byte, arguments ...string) string {
		return gitInputOutput(t, work, input, arguments...)
	}
	blob := func(content string) string {
		return object([]byte(content), "hash-object", "-w", "--stdin")
	}
	tree := func(input string) string {
		return object([]byte(input), "mktree")
	}
	commit := func(treeOID, message string) string {
		return object([]byte(message+"\n"), "-c", "user.name=Restore Fixture", "-c", "user.email=restore@example.invalid", "commit-tree", treeOID)
	}

	first := blob("first unselected record\n")
	second := blob("second unselected record\n")
	validZ := blob("valid z\n")
	malformedZ := blob("malformed z\n")
	firstLeaf := tree("100644 blob " + first + "\tc\n")
	firstParent := tree("040000 tree " + firstLeaf + "\tb\n")
	secondLeaf := tree("100644 blob " + second + "\tc\n")

	var raw bytes.Buffer
	writeEntry := func(mode, name, oid string) {
		raw.WriteString(mode + " " + name)
		raw.WriteByte(0)
		decoded, err := hex.DecodeString(oid)
		if err != nil {
			t.Fatalf("decode object ID %q: %v", oid, err)
		}
		raw.Write(decoded)
	}
	writeEntry("40000", "a", firstParent)
	writeEntry("40000", "a/b", secondLeaf)
	writeEntry("100644", "z", malformedZ)
	malformedTree := object(raw.Bytes(), "hash-object", "--literally", "-t", "tree", "-w", "--stdin")
	validTree := tree("100644 blob " + validZ + "\tz\n")
	return commit(validTree, "valid tree"), commit(malformedTree, "duplicate flattened path")
}

func TestSelectedRestoreRejectsDuplicateFlattenedPaths(t *testing.T) {
	for _, malformedSource := range []bool{false, true} {
		name := "target tree"
		if malformedSource {
			name = "source tree"
		}
		t.Run(name, func(t *testing.T) {
			manager, remote, work := newTestRepository(t)
			valid, malformed := duplicateFlattenedPathCommits(t, work)
			source, target := valid, malformed
			if malformedSource {
				source, target = malformed, valid
			}
			runGit(t, work, "push", "origin", source+":refs/heads/source", target+":refs/heads/main")

			beforeObjects := gitOutput(t, remote, "--git-dir", ".", "count-objects", "-v")
			beforeRecords := gitOutput(t, remote, "--git-dir", ".", "ls-tree", "-r", "--full-tree", malformed)
			if count := strings.Count(beforeRecords, "\ta/b/c"); count != 2 {
				t.Fatalf("duplicate flattened records=%d, want 2:\n%s", count, beforeRecords)
			}
			// The exact malformed-data error proves tree reading stops before a
			// private index can be created in this deliberately missing directory.
			manager.Git.TempDir = filepath.Join(t.TempDir(), "not-created")
			request := RestoreRequest{Source: source, Target: "main", Mode: RestoreFiles, Paths: []string{"z"}, ExpectedHead: target}
			const malformedTreeError = "Git returned malformed restore tree data"
			if _, err := manager.PreviewRestore(context.Background(), "sample", request); err == nil || err.Error() != malformedTreeError {
				t.Errorf("preview error=%v, want %q", err, malformedTreeError)
			}
			if _, err := manager.ApplyRestore(context.Background(), "sample", request); err == nil || err.Error() != malformedTreeError {
				t.Errorf("apply error=%v, want %q", err, malformedTreeError)
			}

			assertRef(t, remote, "refs/heads/source", source)
			assertRef(t, remote, "refs/heads/main", target)
			if after := gitOutput(t, remote, "--git-dir", ".", "count-objects", "-v"); after != beforeObjects {
				t.Errorf("refused restore wrote objects: before=%s after=%s", beforeObjects, after)
			}
			if after := gitOutput(t, remote, "--git-dir", ".", "ls-tree", "-r", "--full-tree", malformed); after != beforeRecords {
				t.Errorf("refused restore changed duplicate records:\nbefore:\n%s\nafter:\n%s", beforeRecords, after)
			}
		})
	}
}

func TestSelectedRestoreRefusesNonAdjacentPathCollisions(t *testing.T) {
	for _, test := range []struct {
		name, prefix, child string
		directorySource     bool
	}{
		{name: "file replaces unselected descendant", child: "child"},
		{name: "file replaces distant unselected descendant", child: "inner/child"},
		{name: "deep file replaces unselected descendant", prefix: "deep/nested/", child: "child"},
		{name: "directory entry under unselected file", child: "child", directorySource: true},
		{name: "deep directory entry under unselected file", prefix: "deep/nested/", child: "inner/child", directorySource: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, remote, _ := newTestRepository(t)
			source, target := restoreCollisionCommits(t, remote, test.prefix, test.child)
			selected := test.prefix + "node"
			if test.directorySource {
				source, target = target, source
				selected += "/" + test.child
			}
			runGit(t, remote, "--git-dir", ".", "update-ref", "refs/heads/main", target)
			before := gitOutput(t, remote, "--git-dir", ".", "count-objects", "-v")
			// A refusal must happen before even creating the private index.
			manager.Git.TempDir = filepath.Join(t.TempDir(), "not-created")
			request := RestoreRequest{Source: source, Target: "main", Mode: RestoreFiles, Paths: []string{selected}, ExpectedHead: target}
			if _, err := manager.PreviewRestore(context.Background(), "sample", request); !errors.Is(err, ErrRestoreUnsupported) {
				t.Errorf("preview error=%v, want unsupported before creating an index", err)
			}
			if _, err := manager.ApplyRestore(context.Background(), "sample", request); !errors.Is(err, ErrRestoreUnsupported) {
				t.Errorf("apply error=%v, want unsupported before creating an index", err)
			}
			assertRef(t, remote, "refs/heads/main", target)
			if after := gitOutput(t, remote, "--git-dir", ".", "count-objects", "-v"); after != before {
				t.Errorf("refused restore wrote objects: before=%s after=%s", before, after)
			}
			if !test.directorySource {
				if got := gitOutput(t, remote, "--git-dir", ".", "show", "main:"+test.prefix+"node/"+test.child); got != "synthetic data" {
					t.Errorf("unselected descendant content=%q", got)
				}
			}
		})
	}
}

func TestSelectedRestoreAllowsExplicitFileDirectoryReplacement(t *testing.T) {
	for _, directorySource := range []bool{false, true} {
		name := "file replaces selected descendants"
		if directorySource {
			name = "directory replaces selected file"
		}
		t.Run(name, func(t *testing.T) {
			manager, remote, _ := newTestRepository(t)
			source, target := restoreCollisionCommits(t, remote, "deep/nested/", "inner/child")
			if directorySource {
				source, target = target, source
			}
			runGit(t, remote, "--git-dir", ".", "update-ref", "refs/heads/main", target)
			request := RestoreRequest{Source: source, Target: "main", Mode: RestoreFiles, Paths: []string{"deep/nested/node", "deep/nested/node/inner/child"}}
			preview, err := manager.PreviewRestore(context.Background(), "sample", request)
			noErr(t, err)
			request.ExpectedHead = preview.ExpectedHead
			applied, err := manager.ApplyRestore(context.Background(), "sample", request)
			noErr(t, err)
			if got, want := gitOutput(t, remote, "--git-dir", ".", "rev-parse", applied.CommitOID+"^{tree}"), gitOutput(t, remote, "--git-dir", ".", "rev-parse", source+"^{tree}"); got != want {
				t.Errorf("selected replacement tree=%s, want %s", got, want)
			}
		})
	}
}

func TestSelectedRestorePreservesPrefixNeighbors(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	for _, name := range []string{"node", "node-neighbor", "node.txt"} {
		noErr(t, os.WriteFile(filepath.Join(work, name), []byte("source\n"), 0o600))
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "prefix neighbors")
	source := gitOutput(t, work, "rev-parse", "HEAD")
	noErr(t, os.WriteFile(filepath.Join(work, "node"), []byte("target\n"), 0o600))
	runGit(t, work, "add", "node")
	runGit(t, work, "commit", "-m", "changed node")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	request := RestoreRequest{Source: source, Target: "main", Mode: RestoreFiles, Paths: []string{"node"}}
	preview, err := manager.PreviewRestore(context.Background(), "sample", request)
	noErr(t, err)
	if len(preview.Changes) != 1 || preview.Changes[0].Path != "node" {
		t.Fatalf("prefix neighbor preview changed unselected files: %+v", preview.Changes)
	}
	request.ExpectedHead = preview.ExpectedHead
	applied, err := manager.ApplyRestore(context.Background(), "sample", request)
	noErr(t, err)
	if got, want := gitOutput(t, remote, "--git-dir", ".", "rev-parse", applied.CommitOID+"^{tree}"), gitOutput(t, remote, "--git-dir", ".", "rev-parse", source+"^{tree}"); got != want {
		t.Errorf("prefix neighbor restore tree=%s, want %s", got, want)
	}
}

func TestSelectedRestoreRechecksChangedTarget(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	source, collision := restoreCollisionCommits(t, remote, "", "child")
	safeTree := gitInputOutput(t, remote, nil, "--git-dir", ".", "mktree")
	safe := gitInputOutput(t, remote, []byte("safe target\n"), "--git-dir", ".", "-c", "user.name=Restore Fixture", "-c", "user.email=restore@example.invalid", "commit-tree", safeTree)
	runGit(t, remote, "--git-dir", ".", "update-ref", "refs/heads/main", safe)
	request := RestoreRequest{Source: source, Target: "main", Mode: RestoreFiles, Paths: []string{"node"}}
	preview, err := manager.PreviewRestore(context.Background(), "sample", request)
	noErr(t, err)
	runGit(t, remote, "--git-dir", ".", "update-ref", "refs/heads/main", collision, safe)
	request.ExpectedHead = preview.ExpectedHead
	if _, err := manager.ApplyRestore(context.Background(), "sample", request); !errors.Is(err, ErrRestoreConflict) {
		t.Fatalf("stale preview apply error=%v, want conflict", err)
	}
	// Even with the current tip supplied, apply validates the selection again.
	request.ExpectedHead = collision
	if _, err := manager.ApplyRestore(context.Background(), "sample", request); !errors.Is(err, ErrRestoreUnsupported) {
		t.Fatalf("changed target apply error=%v, want unsupported", err)
	}
	assertRef(t, remote, "refs/heads/main", collision)
	if got := gitOutput(t, remote, "--git-dir", ".", "show", "main:node/child"); got != "synthetic data" {
		t.Errorf("unselected descendant content=%q", got)
	}
}
