package repository

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/testfixture"
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
	noErr(t, err)
	fullTag, resolvedTag, err := manager.ResolveRef(context.Background(), "sample", "refs/tags/collision")
	noErr(t, err)
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
	content := "<script>alert('not markup')</script>\nsecond line\n"
	rawDirectory := `raw\directory`
	rawFile := `back\slash.txt`
	filePath := "dir/" + rawDirectory + "/" + rawFile
	blobOID := gitInputOutput(t, work, []byte(content), "hash-object", "-w", "--stdin")
	directoryTree := gitInputOutput(t, work, []byte("100644 blob "+blobOID+"\t"+rawFile+"\x00"), "mktree", "-z")
	nestedTree := gitInputOutput(t, work, []byte("040000 tree "+directoryTree+"\t"+rawDirectory+"\x00"), "mktree", "-z")
	rootTree := gitInputOutput(t, work, []byte("040000 tree "+nestedTree+"\tdir\x00"), "mktree", "-z")
	messagePath := filepath.Join(t.TempDir(), "message")
	noErr(t, os.WriteFile(messagePath, []byte("subject with separator\n\nbody "+string(rune(0x1e))+" remains data\n"), 0o600))
	command := exec.Command("git", "commit-tree", rootTree, "-F", messagePath)
	command.Dir = work
	command.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2024-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2024-01-01T00:00:00Z")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("commit special-name tree: %v\n%s", err, output)
	}
	oid := strings.TrimSpace(string(output))
	runGit(t, work, "update-ref", "refs/heads/main", oid)
	runGit(t, work, "push", "origin", "refs/heads/main")

	summary, err := manager.Summary(context.Background(), "sample")
	noErr(t, err)
	if summary.Empty || summary.DefaultBranch != "main" || summary.DefaultOID != oid {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	_, root, err := manager.Tree(context.Background(), "sample", "main", "")
	noErr(t, err)
	if len(root) != 1 || root[0].Name != "dir" || root[0].Type != "tree" {
		t.Fatalf("unexpected root tree: %+v", root)
	}
	_, directory, err := manager.Tree(context.Background(), "sample", "main", "dir")
	noErr(t, err)
	if len(directory) != 1 || directory[0].Name != rawDirectory || directory[0].Type != "tree" {
		t.Fatalf("unexpected directory tree: %+v", directory)
	}
	_, nested, err := manager.Tree(context.Background(), "sample", "main", "dir/"+rawDirectory)
	noErr(t, err)
	if len(nested) != 1 || nested[0].Name != rawFile || nested[0].Path != filePath {
		t.Fatalf("unexpected nested raw-name tree: %+v", nested)
	}
	_, blob, err := manager.ReadBlob(context.Background(), "sample", "main", filePath, 2<<20)
	noErr(t, err)
	if string(blob.Content) != content || blob.Binary || blob.Truncated {
		t.Fatalf("unexpected blob: %+v", blob)
	}
	_, commits, err := manager.Commits(context.Background(), "sample", "main", 10)
	noErr(t, err)
	if len(commits) != 1 || commits[0].OID != oid || !strings.Contains(commits[0].Body, string(rune(0x1e))) {
		t.Fatalf("commit metadata lost untrusted control data: %+v", commits)
	}
	commit, files, err := manager.CommitFiles(context.Background(), "sample", oid)
	noErr(t, err)
	if len(files) != 1 || files[0].Path != filePath || files[0].Status != "added" || files[0].Additions != 2 {
		t.Fatalf("unexpected changed files: %+v", files)
	}
	if commit.OID != oid || commit.CommitterName == "" || !strings.Contains(commit.Body, string(rune(0x1e))) {
		t.Fatalf("unexpected commit metadata: %+v", commit)
	}
	patch, truncated, err := manager.CommitPatch(context.Background(), "sample", oid, files[0].Path, nil, 1<<20)
	noErr(t, err)
	if truncated || !strings.Contains(patch, "+<script>alert('not markup')</script>") {
		t.Fatalf("unexpected commit patch: truncated=%v %q", truncated, patch)
	}
	activity, err := manager.Activity(context.Background(), "sample", 100)
	if err != nil || activity.Incomplete || len(activity.Records) != 1 || activity.Records[0].OID != oid || activity.Records[0].Source != "refs/heads/main" {
		t.Fatalf("unexpected activity records: records=%+v incomplete=%v err=%v", activity.Records, activity.Incomplete, err)
	}
}

func TestDeepTreeLookupUsesBoundedGitProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	manager, remote, _ := newTestRepository(t)
	components := make([]string, 256)
	for index := range components {
		components[index] = fmt.Sprintf("level%03d", index)
	}
	directory := strings.Join(components, "/")
	filePath := directory + "/deep.txt"
	fixture := "blob\nmark :1\ndata 5\ndeep\n" +
		"commit refs/heads/deep\ncommitter Deep Test <deep@example.invalid> 1704067200 +0000\ndata 4\ndeep\n" +
		"M 100644 :1 " + filePath + "\n\ndone\n"
	command := exec.Command("git", "--git-dir", remote, "fast-import", "--quiet")
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	command.Stdin = strings.NewReader(fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create deep tree fixture: %v\n%s", err, output)
	}

	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	tracePath := filepath.Join(t.TempDir(), "git-commands")
	wrapperPath := filepath.Join(t.TempDir(), "git-wrapper")
	wrapper := "#!/bin/sh\nprintf '%s\\0' \"$@\" >> " + shellQuote(tracePath) + "\nprintf '\\n' >> " + shellQuote(tracePath) + "\nexec " + shellQuote(gitPath) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapperPath, []byte(wrapper), 0o700))
	traced, err := gitexec.New(wrapperPath, filepath.Join(t.TempDir(), "runtime"))
	noErr(t, err)
	manager.Git = traced

	resetTrace := func() {
		t.Helper()
		noErr(t, os.WriteFile(tracePath, nil, 0o600))
	}
	lsTreeCalls := func() int {
		t.Helper()
		content, err := os.ReadFile(tracePath)
		noErr(t, err)
		return bytes.Count(content, []byte("\x00ls-tree\x00"))
	}

	resetTrace()
	_, entries, err := manager.Tree(context.Background(), "sample", "refs/heads/deep", directory)
	if err != nil || len(entries) != 1 || entries[0].Path != filePath {
		t.Fatalf("deep Tree entries=%+v err=%v", entries, err)
	}
	if calls := lsTreeCalls(); calls != 1 {
		t.Fatalf("deep Tree used %d ls-tree processes, want 1", calls)
	}

	resetTrace()
	_, blob, err := manager.ReadBlob(context.Background(), "sample", "refs/heads/deep", filePath, 1024)
	if err != nil || string(blob.Content) != "deep\n" {
		t.Fatalf("deep ReadBlob=%q err=%v", blob.Content, err)
	}
	if calls := lsTreeCalls(); calls != 1 {
		t.Fatalf("deep ReadBlob used %d ls-tree processes, want 1", calls)
	}
}

func TestRefTipsBatchMetadataAcrossRefs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "base", "base", "2024-01-01T00:00:00Z")
	base := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	branchOIDs := make(map[string]string)
	for index := 0; index < 5; index++ {
		name := "branch-" + strconv.Itoa(index)
		commitFile(t, work, name, name, fmt.Sprintf("2024-01-0%dT00:00:00Z", index+2))
		branchOIDs[name] = gitOutput(t, work, "rev-parse", "HEAD")
		runGit(t, work, "push", "origin", "HEAD:refs/heads/"+name)
	}
	runGit(t, work, "tag", "lightweight", base)
	runGit(t, work, "tag", "-a", "annotated", "-m", "annotated", base)
	inner := gitOutput(t, work, "rev-parse", "refs/tags/annotated")
	runGit(t, work, "tag", "-a", "nested", "-m", "nested", inner)
	blob := gitInputOutput(t, work, []byte("blob\n"), "hash-object", "-w", "--stdin")
	runGit(t, work, "tag", "-a", "blobtag", "-m", "blobtag", blob)
	runGit(t, work, "push", "origin", "refs/tags/lightweight", "refs/tags/annotated", "refs/tags/nested", "refs/tags/blobtag")

	summary, err := manager.Summary(context.Background(), "sample")
	noErr(t, err)
	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	tracePath := filepath.Join(t.TempDir(), "git-commands")
	wrapperPath := filepath.Join(t.TempDir(), "git-wrapper")
	wrapper := "#!/bin/sh\nprintf '%s\\0' \"$@\" >> " + shellQuote(tracePath) + "\nprintf '\\n' >> " + shellQuote(tracePath) + "\nexec " + shellQuote(gitPath) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapperPath, []byte(wrapper), 0o700))
	traced, err := gitexec.New(wrapperPath, filepath.Join(t.TempDir(), "runtime"))
	noErr(t, err)
	manager.Git = traced
	noErr(t, os.WriteFile(tracePath, nil, 0o600))

	branchTips, err := manager.RefTips(context.Background(), "sample", summary.Branches)
	noErr(t, err)
	tagTips, err := manager.RefTips(context.Background(), "sample", summary.Tags)
	noErr(t, err)
	if len(branchTips) != len(summary.Branches) {
		t.Fatalf("branch tips=%d, want %d", len(branchTips), len(summary.Branches))
	}
	for name, oid := range branchOIDs {
		if branchTips[name].OID != oid {
			t.Fatalf("branch %s tip=%q, want %q", name, branchTips[name].OID, oid)
		}
	}
	for _, name := range []string{"lightweight", "annotated", "nested"} {
		if tagTips[name].OID != base {
			t.Fatalf("tag %s tip=%q, want %q", name, tagTips[name].OID, base)
		}
	}
	if _, ok := tagTips["blobtag"]; ok {
		t.Fatal("blob tag unexpectedly produced a commit tip")
	}
	trace, err := os.ReadFile(tracePath)
	noErr(t, err)
	if count := bytes.Count(trace, []byte("\x00log\x00")); count != 2 {
		t.Fatalf("RefTips used %d log processes, want 2", count)
	}
	if count := bytes.Count(trace, []byte("\x00cat-file\x00")); count != 1 {
		t.Fatalf("RefTips used %d cat-file processes, want 1", count)
	}
}

func gitInputOutput(t *testing.T, directory string, input []byte, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = testfixture.GitEnvironment(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))
	command.Stdin = strings.NewReader(string(input))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
