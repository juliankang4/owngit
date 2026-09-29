package pullrequest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
)

// A mergeability answer is about the exact current pair, follows the rule a
// merge follows, and leaves the repository and the records as they were.
func TestMergeabilityAnswersForTheCurrentPairAndWritesNothing(t *testing.T) {
	for _, format := range []string{repository.ObjectFormatSHA1, repository.ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			fixture := newServiceFixtureWithFormat(t, format)
			fixture.commitFile("base.txt", "base\n", "base")
			fixture.push("HEAD:refs/heads/main")
			fixture.git("checkout", "-b", "feature")
			fixture.commitFile("feature.txt", "feature\n", "feature")
			fixture.push("HEAD:refs/heads/feature")
			created, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Feature", SourceBranch: "feature", TargetBranch: "main"})
			noErr(t, err)
			number := created.Number

			first := fixture.askMergeability(number, RevisionInput{})
			if first.Status != MergeabilityClean || first.Method != "fast_forward" || first.Source.OID != fixture.ref("refs/heads/feature") || first.Target.OID != fixture.ref("refs/heads/main") {
				t.Fatalf("fast-forward answer %+v", first)
			}
			firstPair := RevisionInput{SourceOID: first.Source.OID, TargetOID: first.Target.OID}
			if again := fixture.askMergeability(number, firstPair); again.Status != MergeabilityClean {
				t.Fatalf("answer for the unchanged pair %+v", again)
			}

			// The target moves: the earlier answer no longer applies.
			fixture.git("checkout", "main")
			fixture.commitFile("main.txt", "main\n", "main")
			fixture.push("HEAD:refs/heads/main")
			stale := fixture.askMergeability(number, firstPair)
			if stale.Status != MergeabilityStale || stale.Target.OID != fixture.ref("refs/heads/main") || stale.Source.OID != firstPair.SourceOID || stale.Method != "" {
				t.Fatalf("answer after the target moved %+v", stale)
			}
			diverged := fixture.askMergeability(number, RevisionInput{})
			if diverged.Status != MergeabilityClean || diverged.Method != "merge_commit" {
				t.Fatalf("diverged answer %+v", diverged)
			}
			divergedPair := RevisionInput{SourceOID: diverged.Source.OID, TargetOID: diverged.Target.OID}

			// Both branches change the same lines in more files than an
			// answer lists.
			for index := 0; index <= MaximumConflictPaths; index++ {
				writeWorkFile(t, fixture, fmt.Sprintf("conflict/%03d.txt", index), "main\n")
			}
			fixture.git("add", "-A")
			fixture.git("commit", "-m", "main side")
			fixture.push("HEAD:refs/heads/main")
			fixture.git("checkout", "feature")
			for index := 0; index <= MaximumConflictPaths; index++ {
				writeWorkFile(t, fixture, fmt.Sprintf("conflict/%03d.txt", index), "feature\n")
			}
			fixture.git("add", "-A")
			fixture.git("commit", "-m", "feature side")
			fixture.push("HEAD:refs/heads/feature")

			before := repositorySnapshot(t, fixture.remote)
			conflict := fixture.askMergeability(number, RevisionInput{})
			if conflict.Status != MergeabilityConflict || conflict.Method != "" || !conflict.ConflictPathsTruncated ||
				len(conflict.ConflictPaths) != MaximumConflictPaths || conflict.ConflictPaths[0] != "conflict/000.txt" {
				t.Fatalf("conflict answer status=%s method=%q truncated=%v paths=%d first=%v",
					conflict.Status, conflict.Method, conflict.ConflictPathsTruncated, len(conflict.ConflictPaths), conflict.ConflictPaths[:min(1, len(conflict.ConflictPaths))])
			}
			if after := repositorySnapshot(t, fixture.remote); !reflect.DeepEqual(before, after) {
				t.Fatalf("the check changed the repository:\n%s", snapshotDifference(before, after))
			}
			if left := mergeabilityScratch(t, fixture.manager.Git); len(left) != 0 {
				t.Fatalf("the check left temporary folders: %v", left)
			}
			if _, recorded, err := fixture.store.PullRequestMergeIntent(fixture.ctx, fixture.repositoryID, number, divergedPair.SourceOID, divergedPair.TargetOID); err != nil || recorded {
				t.Fatalf("the check recorded a merge intent: recorded=%v err=%v", recorded, err)
			}
			for _, name := range []string{MergeTreeRef(number, divergedPair.SourceOID, divergedPair.TargetOID), MergeReceiptRef(number)} {
				if fixture.refExists(name) {
					t.Fatalf("the check wrote %s", name)
				}
			}

			// The source moves: the diverged answer no longer applies.
			if moved := fixture.askMergeability(number, divergedPair); moved.Status != MergeabilityStale || moved.Source.OID == divergedPair.SourceOID {
				t.Fatalf("answer after the source moved %+v", moved)
			}
			if _, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, number, RevisionInput{SourceOID: conflict.Source.OID, TargetOID: conflict.Target.OID}); problemCode(err) != "merge_conflict" {
				t.Fatalf("merge of the conflicting pair err=%v", err)
			}
		})
	}
}

func TestMergeabilityOfUnrelatedHistoriesIsAConflict(t *testing.T) {
	fixture := newServiceFixture(t)
	fixture.commitFile("main.txt", "main\n", "main root")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "--orphan", "isolated")
	fixture.git("rm", "-rf", ".")
	fixture.commitFile("isolated.txt", "isolated\n", "isolated root")
	fixture.push("HEAD:refs/heads/isolated")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Unrelated", SourceBranch: "isolated", TargetBranch: "main"})
	noErr(t, err)
	if answer := fixture.askMergeability(created.Number, RevisionInput{}); answer.Status != MergeabilityConflict || answer.Reason != "no_merge_base" || len(answer.ConflictPaths) != 0 {
		t.Fatalf("unrelated histories answer %+v", answer)
	}
}

// When OwnGit cannot tell, the answer says so and why; it is never clean or
// a conflict.
func TestMergeabilityUnavailable(t *testing.T) {
	fixture, number := newDivergedMergeabilityFixture(t)
	fixture.git("push", "origin", ":refs/heads/feature")
	if answer := fixture.askMergeability(number, RevisionInput{}); answer.Status != MergeabilityUnavailable || answer.Reason != "source_branch_missing" {
		t.Fatalf("answer without the source branch %+v", answer)
	}

	if runtime.GOOS == "windows" {
		t.Skip("the Git stand-ins are shell scripts")
	}
	for name, test := range map[string]struct {
		script string
		reason string
	}{
		"old Git": {"if test \"$1\" = --version; then echo 'git version 2.37.6'; exit 0; fi\n", "unsupported_git"},
		"failing merge": {"for arg in \"$@\"; do\n  if test \"$arg\" = merge-tree; then echo 'fatal: synthetic failure' >&2; exit 128; fi\ndone\n", "repository_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture, number := newDivergedMergeabilityFixture(t)
			fixture.manager.Git = gitStandIn(t, test.script, fixture.manager.Git)
			answer := fixture.askMergeability(number, RevisionInput{})
			if answer.Status != MergeabilityUnavailable || answer.Reason != test.reason || answer.Method != "" || answer.ConflictPaths != nil || answer.Message == "" {
				t.Fatalf("answer %+v", answer)
			}
		})
	}
}

func TestMergeabilityOfAClosedPullRequestIsRefused(t *testing.T) {
	fixture, number := newDivergedMergeabilityFixture(t)
	_, err := fixture.service.Close(fixture.ctx, fixture.repositoryID, number)
	noErr(t, err)
	if _, err := fixture.service.Mergeability(fixture.ctx, fixture.repositoryID, number, RevisionInput{}); problemCode(err) != "pull_request_not_open" {
		t.Fatalf("closed pull request err=%v", err)
	}
	if _, err := fixture.service.Mergeability(fixture.ctx, fixture.repositoryID, number, RevisionInput{SourceOID: "abc"}); problemCode(err) != "invalid_revision" {
		t.Fatalf("half a pair err=%v", err)
	}
}

// newDivergedMergeabilityFixture opens a pull request whose branches both
// changed since they split, so a merge needs a merge commit.
func newDivergedMergeabilityFixture(t *testing.T) (*serviceFixture, int64) {
	t.Helper()
	fixture := newServiceFixture(t)
	fixture.commitFile("base.txt", "base\n", "base")
	fixture.push("HEAD:refs/heads/main")
	fixture.git("checkout", "-b", "feature")
	fixture.commitFile("feature.txt", "feature\n", "feature")
	fixture.push("HEAD:refs/heads/feature")
	fixture.git("checkout", "main")
	fixture.commitFile("main.txt", "main\n", "main")
	fixture.push("HEAD:refs/heads/main")
	created, err := fixture.service.Create(fixture.ctx, CreateInput{Repository: fixture.repositoryID, Title: "Diverged", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	return fixture, created.Number
}

func (fixture *serviceFixture) askMergeability(number int64, expected RevisionInput) *Mergeability {
	fixture.t.Helper()
	answer, err := fixture.service.Mergeability(fixture.ctx, fixture.repositoryID, number, expected)
	noErr(fixture.t, err, "mergeability")
	if !answer.OK || answer.Repository != fixture.repositoryID || answer.Number != number {
		fixture.t.Fatalf("answer header %+v", answer)
	}
	return answer
}

// gitStandIn returns a runner whose Git runs script first and otherwise the
// real Git, in the runtime folders of runner.
func gitStandIn(t *testing.T, script string, runner *gitexec.Runner) *gitexec.Runner {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	noErr(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script+"exec "+shellQuote(runner.GitPath)+" \"$@\"\n"), 0o700))
	standIn := *runner
	standIn.GitPath = path
	return &standIn
}

func writeWorkFile(t *testing.T, fixture *serviceFixture, name, content string) {
	t.Helper()
	path := filepath.Join(fixture.work, filepath.FromSlash(name))
	noErr(t, os.MkdirAll(filepath.Dir(path), 0o700))
	noErr(t, os.WriteFile(path, []byte(content), 0o600))
}

// repositorySnapshot is every folder and file below root, with each file's
// content digest.
func repositorySnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	noErr(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[relative+"/"] = ""
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		snapshot[relative] = hex.EncodeToString(digest[:])
		return nil
	}))
	return snapshot
}

func snapshotDifference(before, after map[string]string) string {
	var lines []string
	for path, digest := range after {
		if old, ok := before[path]; !ok {
			lines = append(lines, "added "+path)
		} else if old != digest {
			lines = append(lines, "changed "+path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			lines = append(lines, "removed "+path)
		}
	}
	return strings.Join(lines, "\n")
}

func mergeabilityScratch(t *testing.T, runner *gitexec.Runner) []string {
	t.Helper()
	entries, err := os.ReadDir(runner.TempDir)
	noErr(t, err)
	var left []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "mergeability-") {
			left = append(left, entry.Name())
		}
	}
	return left
}
