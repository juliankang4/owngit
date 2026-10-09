package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"owngit/internal/testfixture"
)

func pathTreeCommit(t *testing.T, remote, tree string, parents ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	args := []string{"--git-dir", remote, "commit-tree", tree}
	for _, parent := range parents {
		args = append(args, "-p", parent)
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Env = append(testfixture.GitEnvironment(os.Environ()), "GIT_AUTHOR_NAME=Path Test", "GIT_AUTHOR_EMAIL=paths@example.invalid", "GIT_COMMITTER_NAME=Path Test", "GIT_COMMITTER_EMAIL=paths@example.invalid", "GIT_AUTHOR_DATE=2024-01-02T00:00:00Z", "GIT_COMMITTER_DATE=2024-01-02T00:00:00Z")
	command.Stdin = strings.NewReader("path fixture\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("commit tree: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestChangedPaths(t *testing.T) {
	manager, remote, _ := newTestRepository(t)
	blob := hashBareBlob(t, remote, []byte("synthetic\n"))
	entry := rawTreeEntry{mode: "100644", objectType: "blob", oid: blob}
	baseTree := makeBareTree(t, remote, map[string]rawTreeEntry{"base.txt": entry})
	base := pathTreeCommit(t, remote, baseTree)
	headTree := makeBareTree(t, remote, map[string]rawTreeEntry{"base.txt": entry, "src.txt": entry, "name\nwith space.txt": entry})
	head := pathTreeCommit(t, remote, headTree, base)
	targetTree := makeBareTree(t, remote, map[string]rawTreeEntry{"base.txt": entry, "target.txt": entry})
	target := pathTreeCommit(t, remote, targetTree, base)
	unrelated := pathTreeCommit(t, remote, targetTree)
	left := pathTreeCommit(t, remote, headTree, base)
	right := pathTreeCommit(t, remote, targetTree, base)
	mergeOne := pathTreeCommit(t, remote, headTree, left, right)
	mergeTwo := pathTreeCommit(t, remote, targetTree, right, left)
	many := map[string]rawTreeEntry{}
	for index := range MaximumChangedPaths + 1 {
		many[fmt.Sprintf("file-%04d", index)] = entry
	}
	fileCut := pathTreeCommit(t, remote, makeBareTree(t, remote, many), base)
	large := map[string]rawTreeEntry{}
	for index := range 4000 {
		large[fmt.Sprintf("%04d-", index)+strings.Repeat("a", 250)] = entry
	}
	byteCut := pathTreeCommit(t, remote, makeBareTree(t, remote, large), base)
	for _, test := range []struct {
		name, base, head string
		request          PathChangeRequest
		complete         bool
		count            int
		error            bool
	}{
		{"two dot", base, head, PathChangeRequest{PreviousOID: base}, true, 2, false},
		{"identical trees", head, head, PathChangeRequest{PreviousOID: head}, true, 0, false},
		{"new branch merge base", target, head, PathChangeRequest{DefaultOID: target}, true, 2, false},
		{"first default push", head, head, PathChangeRequest{DefaultOID: head}, true, 3, false},
		{"empty repository base", head, head, PathChangeRequest{}, true, 3, false},
		{"unrelated new branch", unrelated, head, PathChangeRequest{DefaultOID: unrelated}, true, 3, false},
		{"pull request three dot", target, head, PathChangeRequest{PullRequest: true}, true, 2, false},
		{"pull request no base", unrelated, head, PathChangeRequest{PullRequest: true}, false, 0, false},
		{"pull request several bases", mergeOne, mergeTwo, PathChangeRequest{PullRequest: true}, false, 0, false},
		{"missing previous commit", base, head, PathChangeRequest{PreviousOID: strings.Repeat("a", 40)}, false, 0, false},
		{"not an object ID", base, head, PathChangeRequest{PreviousOID: "--no-index"}, false, 0, false},
		{"file bound", base, fileCut, PathChangeRequest{PreviousOID: base}, false, MaximumChangedPaths, false},
		{"byte bound", base, byteCut, PathChangeRequest{PreviousOID: base}, false, -1, false},
		{"cancelled context", base, head, PathChangeRequest{PreviousOID: base}, false, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			pinned, err := manager.PinRepository(ctx, "sample", test.base, test.head)
			noErr(t, err)
			if test.error {
				cancel()
			}
			changed, err := pinned.ChangedPaths(ctx, test.request)
			if test.error {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error=%v", err)
				}
				return
			}
			if err != nil || changed.Complete != test.complete || test.count >= 0 && len(changed.Files) != test.count || !test.complete && changed.Reason == "" {
				t.Fatalf("complete=%v paths=%d reason=%q err=%v", changed.Complete, len(changed.Files), changed.Reason, err)
			}
			if test.count == -1 && (len(changed.Files) == 0 || len(changed.Files) >= MaximumChangedPaths) {
				t.Fatalf("byte bound was not distinct from file bound: %d", len(changed.Files))
			}
			if test.complete && test.count == 2 && (!slices.Contains(changed.Files, "src.txt") || !slices.Contains(changed.Files, "name\nwith space.txt") || slices.Contains(changed.Files, "target.txt")) {
				t.Fatalf("wrong tree comparison: %v", changed.Files)
			}
		})
	}
}
