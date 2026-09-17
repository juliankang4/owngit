package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFullyQualifiedRefsDisambiguateCollidingBranchAndTag(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "tag version", "tag version", "2024-01-01T00:00:00Z")
	tagOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "tag", "collision", tagOID)
	runGit(t, work, "push", "origin", "refs/tags/collision")
	commitFile(t, work, "branch version", "branch version", "2024-01-02T00:00:00Z")
	branchOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/collision")

	fullBranch, resolvedBranch, err := manager.ResolveRef(context.Background(), "sample", "refs/heads/collision")
	if err != nil {
		t.Fatal(err)
	}
	fullTag, resolvedTag, err := manager.ResolveRef(context.Background(), "sample", "refs/tags/collision")
	if err != nil {
		t.Fatal(err)
	}
	if fullBranch != "refs/heads/collision" || resolvedBranch != branchOID {
		t.Fatalf("branch resolved as %s %s, want branch %s", fullBranch, resolvedBranch, branchOID)
	}
	if fullTag != "refs/tags/collision" || resolvedTag != tagOID {
		t.Fatalf("tag resolved as %s %s, want tag %s", fullTag, resolvedTag, tagOID)
	}
	shortRef, shortOID, err := manager.ResolveRef(context.Background(), "sample", "collision")
	if err != nil || shortRef != fullBranch || shortOID != branchOID {
		t.Fatalf("legacy short ref did not retain branch-first behavior: ref=%s oid=%s err=%v", shortRef, shortOID, err)
	}
}

func TestBrowseRealTreeBlobCommitAndDiff(t *testing.T) {
	manager, _, work := newTestRepository(t)
	if err := os.Mkdir(filepath.Join(work, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "<script>alert('not markup')</script>\nsecond line\n"
	if err := os.WriteFile(filepath.Join(work, "dir", "back\\slash.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	messagePath := filepath.Join(t.TempDir(), "message")
	if err := os.WriteFile(messagePath, []byte("subject with separator\n\nbody "+string(rune(0x1e))+" remains data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "commit", "-F", messagePath)
	command.Dir = work
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, output)
	}
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	oid := gitOutput(t, work, "rev-parse", "HEAD")

	summary, err := manager.Summary(context.Background(), "sample")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Empty || summary.DefaultBranch != "main" || summary.DefaultOID != oid {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	_, root, err := manager.Tree(context.Background(), "sample", "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(root) != 1 || root[0].Name != "dir" || root[0].Type != "tree" {
		t.Fatalf("unexpected root tree: %+v", root)
	}
	_, directory, err := manager.Tree(context.Background(), "sample", "main", "dir")
	if err != nil {
		t.Fatal(err)
	}
	if len(directory) != 1 || directory[0].Name != "back\\slash.txt" {
		t.Fatalf("unexpected directory tree: %+v", directory)
	}
	_, blob, err := manager.ReadBlob(context.Background(), "sample", "main", "dir/back\\slash.txt", 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(blob.Content) != content || blob.Binary || blob.Truncated {
		t.Fatalf("unexpected blob: %+v", blob)
	}
	_, commits, err := manager.Commits(context.Background(), "sample", "main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].OID != oid || !strings.Contains(commits[0].Body, string(rune(0x1e))) {
		t.Fatalf("commit metadata lost untrusted control data: %+v", commits)
	}
	files, err := manager.ChangedFiles(context.Background(), "sample", oid)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "dir/back\\slash.txt" || files[0].Status != "added" || files[0].Additions != 2 {
		t.Fatalf("unexpected changed files: %+v", files)
	}
	detail, err := manager.Commit(context.Background(), "sample", oid, files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.Diff, "+<script>alert('not markup')</script>") || detail.CommitterName == "" {
		t.Fatalf("unexpected commit detail: %+v", detail)
	}
	records, incomplete, err := manager.ActivityRecords(context.Background(), "sample", 100)
	if err != nil || incomplete || len(records) != 1 || records[0].OID != oid || records[0].Source != "refs/heads/main" {
		t.Fatalf("unexpected activity records: records=%+v incomplete=%v err=%v", records, incomplete, err)
	}
}
