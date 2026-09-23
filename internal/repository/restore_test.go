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

func TestRestoreWholeTreeSelectedFilesCASAndNoOp(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	ctx := context.Background()

	noErr(t, os.WriteFile(filepath.Join(work, "common.txt"), []byte("source\n"), 0o600))
	noErr(t, os.WriteFile(filepath.Join(work, "script.sh"), []byte("#!/bin/sh\necho restored\n"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(work, "binary.dat"), []byte{0, 1, 2, 255}, 0o600))
	runGit(t, work, "add", ".")
	runGit(t, work, "update-index", "--chmod=+x", "script.sh")
	// Add symlink data through Git so the fixture does not follow or depend on
	// host filesystem symlink support.
	result, err := manager.Git.Run(ctx, work, strings.NewReader("common.txt"), "hash-object", "-w", "--stdin")
	noErr(t, err)
	linkBlob := strings.TrimSpace(string(result.Stdout))
	runGit(t, work, "update-index", "--add", "--cacheinfo", "120000,"+linkBlob+",link")
	runGit(t, work, "commit", "-m", "source tree")
	sourceOID := gitOutput(t, work, "rev-parse", "HEAD")

	noErr(t, os.WriteFile(filepath.Join(work, "common.txt"), []byte("target\n"), 0o600))
	noErr(t, os.WriteFile(filepath.Join(work, "deleted.txt"), []byte("target only\n"), 0o600))
	runGit(t, work, "rm", "script.sh", "binary.dat", "link")
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "target tree")
	targetOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	request := RestoreRequest{Source: sourceOID, Target: "main", Mode: RestoreAll}
	preview, err := manager.PreviewRestore(ctx, "sample", request)
	noErr(t, err)
	if preview.ExpectedHead != targetOID || preview.CreatesBranch || !preview.CanApply || len(preview.Changes) != 5 {
		t.Fatalf("unexpected whole-tree preview: %+v", preview)
	}
	request.ExpectedHead = preview.ExpectedHead
	applied, err := manager.ApplyRestore(ctx, "sample", request)
	noErr(t, err)
	if applied.Created || applied.CommitOID == sourceOID || gitOutput(t, "", "--git-dir", remote, "rev-parse", applied.CommitOID+"^") != targetOID {
		t.Fatalf("whole restore did not create a child of the target: %+v", applied)
	}
	if got := gitOutput(t, "", "--git-dir", remote, "rev-parse", applied.CommitOID+"^{tree}"); got != gitOutput(t, "", "--git-dir", remote, "rev-parse", sourceOID+"^{tree}") {
		t.Fatalf("restored tree=%s, want source tree", got)
	}
	entries := gitOutput(t, "", "--git-dir", remote, "ls-tree", applied.CommitOID, "script.sh", "binary.dat", "link")
	for _, want := range []string{"100755 blob", "100644 blob", "120000 blob"} {
		if !strings.Contains(entries, want) {
			t.Fatalf("restored tree entries missing %q:\n%s", want, entries)
		}
	}

	noOp, err := manager.PreviewRestore(ctx, "sample", RestoreRequest{Source: sourceOID, Target: "main", Mode: RestoreAll})
	if err != nil || noOp.CanApply {
		t.Fatalf("same-tree preview canApply=%v err=%v", noOp.CanApply, err)
	}
	_, err = manager.ApplyRestore(ctx, "sample", RestoreRequest{Source: sourceOID, Target: "main", Mode: RestoreAll, ExpectedHead: noOp.ExpectedHead})
	if !errors.Is(err, ErrRestoreNoChanges) {
		t.Fatalf("same-tree apply error=%v, want no changes", err)
	}

	partialTarget := "partial"
	runGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/"+partialTarget, targetOID)
	partialRequest := RestoreRequest{Source: sourceOID, Target: partialTarget, Mode: RestoreFiles, Paths: []string{"common.txt"}}
	partialPreview, err := manager.PreviewRestore(ctx, "sample", partialRequest)
	noErr(t, err)
	partialRequest.ExpectedHead = partialPreview.ExpectedHead
	partialResult, err := manager.ApplyRestore(ctx, "sample", partialRequest)
	noErr(t, err)
	if got := gitOutput(t, "", "--git-dir", remote, "show", partialResult.CommitOID+":common.txt"); got != "source" {
		t.Fatalf("selected file content=%q", got)
	}
	if got := gitOutput(t, "", "--git-dir", remote, "show", partialResult.CommitOID+":deleted.txt"); got != "target only" {
		t.Fatalf("unselected file content=%q", got)
	}
	if _, err := gitCombined("", "--git-dir", remote, "cat-file", "-e", partialResult.CommitOID+":script.sh"); err == nil {
		t.Fatal("selected restore added an unselected source file")
	}

	selectedTarget := "selected"
	runGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/"+selectedTarget, targetOID)
	selectedRequest := RestoreRequest{
		Source: sourceOID, Target: selectedTarget, Mode: RestoreFiles,
		Paths: []string{"common.txt", "script.sh", "binary.dat", "link", "deleted.txt"},
	}
	selectedPreview, err := manager.PreviewRestore(ctx, "sample", selectedRequest)
	noErr(t, err)
	selectedRequest.ExpectedHead = selectedPreview.ExpectedHead
	selectedResult, err := manager.ApplyRestore(ctx, "sample", selectedRequest)
	noErr(t, err)
	if got := gitOutput(t, "", "--git-dir", remote, "rev-parse", selectedResult.CommitOID+"^{tree}"); got != gitOutput(t, "", "--git-dir", remote, "rev-parse", sourceOID+"^{tree}") {
		t.Fatalf("selected restore tree=%s, want source tree", got)
	}
	if got := gitOutput(t, "", "--git-dir", remote, "show", selectedResult.CommitOID+":binary.dat"); got != string([]byte{0, 1, 2, 255}) {
		t.Fatalf("binary content changed: %q", got)
	}
	if got := gitOutput(t, "", "--git-dir", remote, "show", selectedResult.CommitOID+":link"); got != "common.txt" {
		t.Fatalf("symlink blob changed or was followed: %q", got)
	}

	stalePreview, err := manager.PreviewRestore(ctx, "sample", RestoreRequest{Source: targetOID, Target: selectedTarget, Mode: RestoreAll})
	noErr(t, err)
	runGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/"+selectedTarget, targetOID, selectedResult.CommitOID)
	_, err = manager.ApplyRestore(ctx, "sample", RestoreRequest{Source: sourceOID, Target: selectedTarget, Mode: RestoreAll, ExpectedHead: stalePreview.ExpectedHead})
	if !errors.Is(err, ErrRestoreConflict) {
		t.Fatalf("stale apply error=%v, want conflict", err)
	}
	assertRef(t, remote, "refs/heads/"+selectedTarget, targetOID)

	zero := strings.Repeat("0", len(sourceOID))
	missingPreview, err := manager.PreviewRestore(ctx, "sample", RestoreRequest{Source: sourceOID, Target: "recovered", Mode: RestoreAll})
	if err != nil || !missingPreview.CreatesBranch || missingPreview.ExpectedHead != zero {
		t.Fatalf("missing branch preview=%+v err=%v", missingPreview, err)
	}
	created, err := manager.ApplyRestore(ctx, "sample", RestoreRequest{Source: sourceOID, Target: "recovered", Mode: RestoreAll, ExpectedHead: missingPreview.ExpectedHead})
	if err != nil || !created.Created || created.CommitOID != sourceOID {
		t.Fatalf("deleted branch restore=%+v err=%v", created, err)
	}
	_, err = manager.ApplyRestore(ctx, "sample", RestoreRequest{Source: sourceOID, Target: "recovered", Mode: RestoreAll, ExpectedHead: zero})
	if !errors.Is(err, ErrRestoreConflict) {
		t.Fatalf("stale branch creation error=%v, want conflict", err)
	}
}

func TestRestorePatchPreviewTreatsMagicFilenamesLiterally(t *testing.T) {
	manager, _, work := newTestRepository(t)
	ctx := context.Background()
	// The name is invalid on Windows, so the fixture builds raw Git objects
	// instead of writing a working tree file.
	magic := ":(glob)*.txt"
	magicSource := gitInputOutput(t, work, []byte("magic source\n"), "hash-object", "-w", "--stdin")
	otherSource := gitInputOutput(t, work, []byte("other source\n"), "hash-object", "-w", "--stdin")
	sourceTree := gitInputOutput(t, work, []byte("100644 blob "+magicSource+"\t"+magic+"\n100644 blob "+otherSource+"\tother.txt\n"), "mktree")
	sourceOID := gitInputOutput(t, work, []byte("source\n"), "commit-tree", sourceTree)
	magicTarget := gitInputOutput(t, work, []byte("magic target\n"), "hash-object", "-w", "--stdin")
	otherTarget := gitInputOutput(t, work, []byte("other target\n"), "hash-object", "-w", "--stdin")
	targetTree := gitInputOutput(t, work, []byte("100644 blob "+magicTarget+"\t"+magic+"\n100644 blob "+otherTarget+"\tother.txt\n"), "mktree")
	targetOID := gitInputOutput(t, work, []byte("target\n"), "commit-tree", targetTree, "-p", sourceOID)
	runGit(t, work, "push", "origin", targetOID+":refs/heads/main")

	preview, err := manager.PreviewRestore(ctx, "sample", RestoreRequest{Source: sourceOID, Target: "main", Mode: RestoreAll})
	noErr(t, err)
	patch := preview.Patches[magic]
	if patch == "" {
		t.Fatalf("missing patch for %q: %+v", magic, preview.Patches)
	}
	if strings.Contains(patch, "other.txt") {
		t.Fatalf("magic filename patch included an unrelated file:\n%s", patch)
	}
	if !strings.Contains(patch, "magic target") {
		t.Fatalf("magic filename patch lost its own change:\n%s", patch)
	}
}

func TestRestorePublicationReadbackDistinguishesAppliedFailureConflictAndNoChange(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	ctx := context.Background()
	commitFile(t, work, "source", "source", "2024-01-01T00:00:00Z")
	sourceOID := gitOutput(t, work, "rev-parse", "HEAD")
	commitFile(t, work, "target", "target", "2024-01-02T00:00:00Z")
	targetOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	for _, branch := range []string{"applied", "conflict", "unchanged"} {
		runGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/"+branch, targetOID)
	}

	appliedRequest := RestoreRequest{Source: sourceOID, Target: "applied", Mode: RestoreAll, ExpectedHead: targetOID}
	canceledCtx, cancel := context.WithCancel(ctx)
	manager.restorePublisher = func(runCtx context.Context, repositoryPath, targetRef, newOID, expected string) error {
		if _, err := manager.Git.Run(runCtx, "", nil, "--git-dir", repositoryPath, "update-ref", targetRef, newOID, expected); err != nil {
			return err
		}
		cancel()
		return context.Canceled
	}
	result, err := manager.ApplyRestore(canceledCtx, "sample", appliedRequest)
	noErr(t, err, "applied update reported as failure")
	assertRef(t, remote, "refs/heads/applied", result.CommitOID)

	manager.restorePublisher = func(_ context.Context, repositoryPath, targetRef, _ string, expected string) error {
		if _, err := manager.Git.Run(ctx, "", nil, "--git-dir", repositoryPath, "update-ref", targetRef, sourceOID, expected); err != nil {
			return err
		}
		return context.DeadlineExceeded
	}
	_, err = manager.ApplyRestore(ctx, "sample", RestoreRequest{Source: sourceOID, Target: "conflict", Mode: RestoreAll, ExpectedHead: targetOID})
	if !errors.Is(err, ErrRestoreConflict) {
		t.Fatalf("competing update error=%v, want conflict", err)
	}
	assertRef(t, remote, "refs/heads/conflict", sourceOID)

	manager.restorePublisher = func(context.Context, string, string, string, string) error {
		return context.DeadlineExceeded
	}
	_, err = manager.ApplyRestore(ctx, "sample", RestoreRequest{Source: sourceOID, Target: "unchanged", Mode: RestoreAll, ExpectedHead: targetOID})
	if err == nil || errors.Is(err, ErrRestoreConflict) || !strings.Contains(err.Error(), "publish restore commit") {
		t.Fatalf("unchanged update error=%v, want original publication error", err)
	}
	assertRef(t, remote, "refs/heads/unchanged", targetOID)
}

func TestRestoreAllowsExactDanglingCommit(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	ctx := context.Background()
	commitFile(t, work, "target", "target", "2024-01-01T00:00:00Z")
	targetOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	runGit(t, work, "checkout", "--orphan", "detached-source")
	runGit(t, work, "rm", "-rf", ".")
	commitFile(t, work, "dangling", "dangling", "2024-01-02T00:00:00Z")
	sourceOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, "", "--git-dir", remote, "fetch", work, sourceOID)
	if refs := gitOutput(t, "", "--git-dir", remote, "for-each-ref", "--format=%(objectname)"); strings.Contains(refs, sourceOID) {
		t.Fatalf("fixture commit is still referenced: %s", refs)
	}

	request := RestoreRequest{Source: sourceOID, Target: "main", Mode: RestoreAll, ExpectedHead: targetOID}
	result, err := manager.ApplyRestore(ctx, "sample", request)
	noErr(t, err)
	if got := gitOutput(t, "", "--git-dir", remote, "rev-parse", result.CommitOID+"^{tree}"); got != gitOutput(t, "", "--git-dir", remote, "rev-parse", sourceOID+"^{tree}") {
		t.Fatalf("restored tree=%s, want dangling source tree", got)
	}
}

func TestRepositoryIDsArePortableAcrossSupportedPlatforms(t *testing.T) {
	manager, _, _ := newTestRepository(t)
	for _, name := range []string{"foo.git", "CON", "Aux.notes", "NUL", "com1.archive", "Lpt9.data"} {
		id := strings.ToLower(name)
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) accepted an unsupported ID", id)
		}
		if _, err := manager.Create(context.Background(), name, ""); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Create(%q) error=%v, want ErrInvalidName", name, err)
		}
	}
	for _, id := range []string{"console", "com10", "lpt0", "portable.repo"} {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) error=%v", id, err)
		}
	}
}

func TestRestoreRejectsTagGitlinkAndUnsafePathCollision(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	ctx := context.Background()
	noErr(t, os.WriteFile(filepath.Join(work, "node"), []byte("source file\n"), 0o600))
	runGit(t, work, "add", "node")
	runGit(t, work, "commit", "-m", "source file")
	sourceOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "tag", "-a", "annotated", "-m", "annotated", sourceOID)
	tagOID := gitOutput(t, work, "rev-parse", "refs/tags/annotated")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/source", "refs/tags/annotated")

	runGit(t, work, "checkout", "--orphan", "collision-target")
	runGit(t, work, "rm", "-rf", ".")
	noErr(t, os.Mkdir(filepath.Join(work, "node"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(work, "node", "child"), []byte("keep unless selected\n"), 0o600))
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "target directory")
	targetOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/collision-target")

	_, err := manager.PreviewRestore(ctx, "sample", RestoreRequest{Source: sourceOID, Target: "collision-target", Mode: RestoreFiles, Paths: []string{"node"}})
	if !errors.Is(err, ErrRestoreUnsupported) {
		t.Fatalf("unsafe collision error=%v, want unsupported", err)
	}
	_, err = manager.PreviewRestore(ctx, "sample", RestoreRequest{Source: tagOID, Target: "collision-target", Mode: RestoreAll})
	if !errors.Is(err, ErrRestoreInvalid) {
		t.Fatalf("annotated tag error=%v, want invalid", err)
	}

	index := filepath.Join(t.TempDir(), "index")
	env := append(os.Environ(), "GIT_INDEX_FILE="+index)
	readTree := exec.Command("git", "--git-dir", remote, "read-tree", targetOID)
	readTree.Env = env
	if output, err := readTree.CombinedOutput(); err != nil {
		t.Fatalf("read-tree: %v\n%s", err, output)
	}
	update := exec.Command("git", "--git-dir", remote, "update-index", "--add", "--cacheinfo", "160000,"+sourceOID+",module")
	update.Env = env
	if output, err := update.CombinedOutput(); err != nil {
		t.Fatalf("add gitlink: %v\n%s", err, output)
	}
	write := exec.Command("git", "--git-dir", remote, "write-tree")
	write.Env = env
	output, err := write.CombinedOutput()
	if err != nil {
		t.Fatalf("write gitlink tree: %v\n%s", err, output)
	}
	commit := gitOutput(t, "", "-c", "user.name=Restore Test", "-c", "user.email=restore@example.invalid",
		"--git-dir", remote, "commit-tree", strings.TrimSpace(string(output)), "-m", "gitlink")
	_, err = manager.PreviewRestore(ctx, "sample", RestoreRequest{Source: commit, Target: "collision-target", Mode: RestoreFiles, Paths: []string{"module"}})
	if !errors.Is(err, ErrRestoreUnsupported) {
		t.Fatalf("selected gitlink error=%v, want unsupported", err)
	}
}
