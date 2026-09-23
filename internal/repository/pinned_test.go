package repository

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedRepositoryIgnoresMovedRefsAndReplacementObjects(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "base\n", "base", "2024-01-01T00:00:00Z")
	baseOID := gitOutput(t, work, "rev-parse", "HEAD")
	noErr(t, os.WriteFile(filepath.Join(work, ".gitattributes"), []byte("file.txt diff=unsafe filter=unsafe\n"), 0o600))
	runGit(t, work, "add", ".gitattributes")
	commitFile(t, work, "pinned head\n", "head", "2024-01-02T00:00:00Z")
	headOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, "", "--git-dir", remote, "config", "diff.external", "definitely-missing-external-diff")
	runGit(t, "", "--git-dir", remote, "config", "diff.unsafe.textconv", "definitely-missing-textconv")
	runGit(t, "", "--git-dir", remote, "config", "filter.unsafe.smudge", "definitely-missing-smudge-filter")

	pinned, err := manager.PinRepository(context.Background(), "sample", baseOID, headOID)
	noErr(t, err)

	commitFile(t, work, "later branch tip\n", "later", "2024-01-03T00:00:00Z")
	laterOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "+HEAD:refs/heads/main")
	runGit(t, "", "--git-dir", remote, "replace", headOID, laterOID)

	blob, err := pinned.ReadBlob(context.Background(), PinnedHead, "file.txt", 0, 64<<10, 1024, 4096)
	noErr(t, err)
	if string(blob.Content) != "pinned head\n" || blob.HasMore {
		t.Fatalf("pinned head changed after ref/replacement mutation: content=%q more=%v", blob.Content, blob.HasMore)
	}
	change, err := pinned.ReadChange(context.Background(), 64<<10)
	noErr(t, err)
	if !strings.Contains(string(change.Patch), "+pinned head") || strings.Contains(string(change.Patch), "later branch tip") {
		t.Fatalf("pinned diff followed mutable state:\n%s", change.Patch)
	}
	if pinned.BaseOID() != baseOID || pinned.HeadOID() != headOID || pinned.RepositoryID() != "sample" {
		t.Fatalf("unexpected pinned provenance: repository=%q base=%q head=%q", pinned.RepositoryID(), pinned.BaseOID(), pinned.HeadOID())
	}
}

func TestPinnedTreePreservesLiteralPathsAndClassifiesSpecialEntries(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "base\n", "base", "2024-01-01T00:00:00Z")
	baseOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	literalName := `[literal]*?\name.txt`
	literalPath := "odd/" + literalName
	fileOID := hashBareBlob(t, remote, []byte("literal data\n"))
	baseFileOID := hashBareBlob(t, remote, []byte("base\n"))
	linkOID := hashBareBlob(t, remote, []byte("../../outside-sentinel"))
	binaryOID := hashBareBlob(t, remote, []byte{'a', 0, 'b', '\n'})
	oddTreeOID := makeBareTree(t, remote, map[string]rawTreeEntry{
		literalName: {mode: "100644", objectType: "blob", oid: fileOID},
	})
	rootTreeOID := makeBareTree(t, remote, map[string]rawTreeEntry{
		"binary.dat": {mode: "100644", objectType: "blob", oid: binaryOID},
		"file.txt":   {mode: "100644", objectType: "blob", oid: baseFileOID},
		"link":       {mode: "120000", objectType: "blob", oid: linkOID},
		"module":     {mode: "160000", objectType: "commit", oid: baseOID},
		"odd":        {mode: "040000", objectType: "tree", oid: oddTreeOID},
	})
	headOID := commitBareTree(t, remote, rootTreeOID, baseOID)

	pinned, err := manager.PinRepository(context.Background(), "sample", baseOID, headOID)
	noErr(t, err)
	root, err := pinned.ListTree(context.Background(), PinnedHead, "", 64<<10)
	noErr(t, err)
	kinds := make(map[string]string)
	for _, entry := range root {
		kinds[entry.Path] = entry.Type + ":" + entry.Mode
	}
	if kinds["odd"] != "tree:040000" || kinds["link"] != "blob:120000" || kinds["module"] != "commit:160000" {
		t.Fatalf("special entries were not preserved: %+v", kinds)
	}
	odd, err := pinned.ListTree(context.Background(), PinnedHead, "odd", 64<<10)
	if err != nil || len(odd) != 1 || odd[0].Name != literalName || odd[0].Path != literalPath {
		t.Fatalf("literal tree entries=%+v err=%v", odd, err)
	}
	literal, err := pinned.ReadBlob(context.Background(), PinnedHead, literalPath, 0, 64<<10, 1024, 4096)
	if err != nil || string(literal.Content) != "literal data\n" {
		t.Fatalf("literal path read content=%q err=%v", literal.Content, err)
	}
	link, err := pinned.ReadBlob(context.Background(), PinnedHead, "link", 0, 64<<10, 1024, 4096)
	if err != nil || !link.Symlink || string(link.Content) != "../../outside-sentinel" {
		t.Fatalf("symlink was followed or misclassified: %+v err=%v", link, err)
	}
	binary, err := pinned.ReadBlob(context.Background(), PinnedHead, "binary.dat", 0, 64<<10, 1024, 4096)
	if err != nil || !binary.Binary {
		t.Fatalf("binary blob was not classified: %+v err=%v", binary, err)
	}
	if _, err := pinned.ReadBlob(context.Background(), PinnedHead, "module", 0, 64<<10, 1024, 4096); !errors.Is(err, ErrPinnedUnsupportedObject) {
		t.Fatalf("submodule read err=%v, want unsupported object", err)
	}
	if _, err := pinned.ListTree(context.Background(), PinnedHead, "module", 64<<10); !errors.Is(err, ErrPinnedUnsupportedObject) {
		t.Fatalf("submodule traversal err=%v, want unsupported object", err)
	}
}

func TestPinnedBlobContinuationAndOutputLimits(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "0123456789abcdef", "base", "2024-01-01T00:00:00Z")
	oid := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	pinned, err := manager.PinRepository(context.Background(), "sample", oid, oid)
	noErr(t, err)

	first, err := pinned.ReadBlob(context.Background(), PinnedHead, "file.txt", 0, 4096, 5, 12)
	noErr(t, err)
	second, err := pinned.ReadBlob(context.Background(), PinnedHead, "file.txt", 5, 4096, 5, 12)
	noErr(t, err)
	third, err := pinned.ReadBlob(context.Background(), PinnedHead, "file.txt", 10, 4096, 5, 12)
	noErr(t, err)
	if string(first.Content) != "01234" || string(second.Content) != "56789" || string(third.Content) != "ab" {
		t.Fatalf("unexpected continuation chunks: %q %q %q", first.Content, second.Content, third.Content)
	}
	if !first.HasMore || !second.HasMore || !third.HasMore {
		t.Fatalf("max-read boundary was not reported: %+v %+v %+v", first, second, third)
	}
	if _, err := pinned.ReadBlob(context.Background(), PinnedHead, "file.txt", 12, 4096, 5, 12); !errors.Is(err, ErrPinnedOutputLimit) {
		t.Fatalf("read beyond explicit maximum err=%v", err)
	}
	if _, err := pinned.ListTree(context.Background(), PinnedHead, "", 1); !errors.Is(err, ErrPinnedOutputLimit) {
		t.Fatalf("tree output limit err=%v", err)
	}
}

func TestPinnedChangeReportsTruncationAndCancellation(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, strings.Repeat("a", 2048), "base", "2024-01-01T00:00:00Z")
	baseOID := gitOutput(t, work, "rev-parse", "HEAD")
	commitFile(t, work, strings.Repeat("b", 2048), "head", "2024-01-02T00:00:00Z")
	headOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	pinned, err := manager.PinRepository(context.Background(), "sample", baseOID, headOID)
	noErr(t, err)
	change, err := pinned.ReadChange(context.Background(), 128)
	noErr(t, err)
	if !change.Truncated || len(change.Patch) != 128 {
		t.Fatalf("bounded diff len=%d truncated=%v", len(change.Patch), change.Truncated)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pinned.ReadChange(ctx, 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read err=%v", err)
	}

	lock := manager.Locks.For("sample")
	lock.Lock()
	_, lockErr := pinned.ReadChange(context.Background(), 1024)
	lock.Unlock()
	if !errors.Is(lockErr, ErrPinnedRepositoryBusy) {
		t.Fatalf("contended read err=%v", lockErr)
	}
}

func TestPinRepositoryRejectsMissingAndNonCommitObjects(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "base", "base", "2024-01-01T00:00:00Z")
	commitOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	blobOID := hashBareBlob(t, remote, []byte("not a commit"))

	for name, oid := range map[string]string{
		"missing":   strings.Repeat("f", 40),
		"noncommit": blobOID,
		"revision":  "refs/heads/main",
		"uppercase": strings.ToUpper(commitOID),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := manager.PinRepository(context.Background(), "sample", commitOID, oid)
			if err == nil {
				t.Fatal("pin unexpectedly succeeded")
			}
		})
	}
	if _, err := manager.PinRepository(context.Background(), "missing", commitOID, commitOID); err == nil || !strings.Contains(err.Error(), "repository not found") {
		t.Fatalf("missing repository err=%v", err)
	}
}

func TestPinnedRepositoryDoesNotRetargetVanishedObject(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "base", "base", "2024-01-01T00:00:00Z")
	baseOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	treeOID := gitOutput(t, "", "--git-dir", remote, "rev-parse", baseOID+"^{tree}")
	headOID := commitBareTree(t, remote, treeOID, baseOID)
	pinned, err := manager.PinRepository(context.Background(), "sample", baseOID, headOID)
	noErr(t, err)
	objectPath := filepath.Join(remote, "objects", headOID[:2], headOID[2:])
	noErr(t, os.Remove(objectPath))
	if _, err := pinned.ReadChange(context.Background(), 4096); !errors.Is(err, ErrPinnedObjectUnavailable) {
		t.Fatalf("vanished head err=%v", err)
	}
	if got := gitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main"); got != baseOID {
		t.Fatalf("branch moved while checking vanished object: got=%s want=%s", got, baseOID)
	}
}

func TestPinnedRepositoryRefusesStorageReplacement(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "base", "base", "2024-01-01T00:00:00Z")
	oid := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	pinned, err := manager.PinRepository(context.Background(), "sample", oid, oid)
	noErr(t, err)

	oldPath := remote + ".old"
	noErr(t, os.Rename(remote, oldPath))
	runGit(t, "", "init", "--bare", remote)
	if _, err := pinned.ListTree(context.Background(), PinnedHead, "", 4096); !errors.Is(err, ErrPinnedRepositoryChanged) {
		t.Fatalf("replacement repository err=%v", err)
	}
}

func TestValidatePinnedPathDeniesHostPathsAndTraversal(t *testing.T) {
	for _, value := range []string{"../outside", "a/../outside", "/tmp/outside", `C:\outside`, `C:/outside`, `\\server\share`, "."} {
		if err := ValidatePinnedPath(value, false); err == nil {
			t.Errorf("path %q unexpectedly accepted", value)
		}
	}
	for _, value := range []string{`odd/[literal]*?\name.txt`, "line\nbreak.txt", "-leading.txt", "percent%2Fname"} {
		if err := ValidatePinnedPath(value, false); err != nil {
			t.Errorf("literal path %q rejected: %v", value, err)
		}
	}
	noErr(t, ValidatePinnedPath("", true), "root path rejected")
	if err := ValidatePinnedPath("", false); err == nil {
		t.Fatal("empty file path unexpectedly accepted")
	}
}

type rawTreeEntry struct {
	mode       string
	objectType string
	oid        string
}

func hashBareBlob(t *testing.T, repositoryPath string, content []byte) string {
	t.Helper()
	command := exec.Command("git", "--git-dir", repositoryPath, "hash-object", "-w", "--stdin")
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	command.Stdin = strings.NewReader(string(content))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("hash bare blob: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func makeBareTree(t *testing.T, repositoryPath string, entries map[string]rawTreeEntry) string {
	t.Helper()
	var input strings.Builder
	for name, entry := range entries {
		input.WriteString(entry.mode)
		input.WriteByte(' ')
		input.WriteString(entry.objectType)
		input.WriteByte(' ')
		input.WriteString(entry.oid)
		input.WriteByte('\t')
		input.WriteString(name)
		input.WriteByte(0)
	}
	command := exec.Command("git", "--git-dir", repositoryPath, "mktree", "-z")
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	command.Stdin = strings.NewReader(input.String())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make bare tree: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func commitBareTree(t *testing.T, repositoryPath, treeOID, parentOID string) string {
	t.Helper()
	command := exec.Command("git", "--git-dir", repositoryPath, "commit-tree", treeOID, "-p", parentOID)
	command.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Pinned Test", "GIT_AUTHOR_EMAIL=pinned@example.invalid",
		"GIT_COMMITTER_NAME=Pinned Test", "GIT_COMMITTER_EMAIL=pinned@example.invalid",
		"GIT_AUTHOR_DATE=2024-01-02T00:00:00Z", "GIT_COMMITTER_DATE=2024-01-02T00:00:00Z",
	)
	command.Stdin = strings.NewReader("pinned tree\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("commit bare tree: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}
