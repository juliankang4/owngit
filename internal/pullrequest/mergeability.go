package pullrequest

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"owngit/internal/gitexec"
	"owngit/internal/state"
)

// Mergeability statuses.
const (
	// MergeabilityClean: merging now succeeds with Method.
	MergeabilityClean = "clean"
	// MergeabilityConflict: merging now is refused as a conflict.
	MergeabilityConflict = "conflict"
	// MergeabilityUnavailable: OwnGit could not tell; Reason says why.
	MergeabilityUnavailable = "unavailable"
	// MergeabilityStale: the pair asked about is no longer the current pair.
	MergeabilityStale = "stale"
)

// MaximumConflictPaths is how many conflicting paths an answer lists.
const MaximumConflictPaths = 100

// Mergeability answers whether a pull request can merge now. The answer is
// about exactly Source.OID and Target.OID and is kept nowhere: once either
// branch moves it no longer applies, and a merge decides again under its own
// lock.
type Mergeability struct {
	OK         bool   `json:"ok"`
	Repository string `json:"repository"`
	Number     int64  `json:"number"`
	Status     string `json:"status"`
	// Source and Target are the current revisions: the pair the answer is
	// about, or, when Status is stale, the pair to ask about instead.
	Source Revision `json:"source"`
	Target Revision `json:"target"`
	// Method is the merge a clean answer would make: fast_forward,
	// merge_commit or up_to_date.
	Method string `json:"method,omitempty"`
	// ConflictPaths are the conflicting paths of a conflict answer, at most
	// MaximumConflictPaths; ConflictPathsTruncated says more exist.
	ConflictPaths          []string `json:"conflict_paths,omitempty"`
	ConflictPathsTruncated bool     `json:"conflict_paths_truncated,omitempty"`
	// Reason is a code: for a conflict, no_merge_base when the branches share
	// no history; for unavailable, why OwnGit could not tell, such as
	// unsupported_git, source_branch_missing or repository_unavailable.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
	// Cause is the failure behind an unavailable answer, for the server log.
	Cause error `json:"-"`
	// Leftover is why the temporary object folder of the check could not be
	// removed, for the server log. The folder is in OwnGit's runtime folder,
	// never in the repository, and the answer stands.
	Leftover error `json:"-"`
}

// Mergeability works out whether pull request number can merge now, for its
// current source and target. When expected names a pair, the answer is stale
// unless that pair is still the current one. Nothing is written: no ref, no
// record, and no object in the repository.
func (service *Service) Mergeability(ctx context.Context, repositoryID string, number int64, expected RevisionInput) (*Mergeability, error) {
	pinned := expected.SourceOID != "" || expected.TargetOID != ""
	if pinned {
		if err := validateExpectedRevision(expected.SourceOID, expected.TargetOID); err != nil {
			return nil, err
		}
	}
	repositoryPath, err := service.repositoryPath(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	lock := service.Repositories.Locks.For(repositoryID)
	if err := lockForRequest(ctx, lock.RLockContext); err != nil {
		return nil, err
	}
	defer lock.RUnlock()
	record, err := service.requirePullRequest(ctx, repositoryID, number)
	if err != nil {
		return nil, err
	}
	if record.Status != state.PullRequestOpen {
		return nil, notOpenProblem(record.Status)
	}
	source, target, err := service.readHeads(ctx, repositoryPath, record)
	if err != nil {
		return nil, err
	}
	answer := &Mergeability{
		OK: true, Repository: repositoryID, Number: number,
		Source: Revision{Branch: source.Branch, OID: source.OID, Status: source.Status},
		Target: Revision{Branch: target.Branch, OID: target.OID, Status: target.Status},
	}
	if eligibility := evaluateEligibility(record.Status, source, target); !eligibility.Eligible {
		answer.Status, answer.Reason, answer.Message = MergeabilityUnavailable, eligibility.Blockers[0].Code, eligibility.Blockers[0].Message
		return answer, nil
	}
	if pinned && (expected.SourceOID != source.OID || expected.TargetOID != target.OID) {
		answer.Status, answer.Message = MergeabilityStale, "The source or target branch moved. Ask again for the current revisions."
		return answer, nil
	}
	if err := service.decideMergeability(ctx, repositoryPath, answer); err != nil {
		problem := AsProblem(err)
		answer.Status, answer.Reason, answer.Message, answer.Cause = MergeabilityUnavailable, problem.Code, problem.Message, err
	}
	return answer, nil
}

// decideMergeability sets answer to clean or conflict by the rule a merge
// follows: mergeMethod, then for a merge commit the same three-way merge.
func (service *Service) decideMergeability(ctx context.Context, repositoryPath string, answer *Mergeability) error {
	if err := service.requireMergeCapability(ctx); err != nil {
		return err
	}
	method, err := service.mergeMethod(ctx, repositoryPath, answer.Source.OID, answer.Target.OID)
	if err != nil {
		return err
	}
	if method == "" {
		answer.Status, answer.Reason, answer.Message = MergeabilityConflict, "no_merge_base", "The source and target branches do not share mergeable history."
		return nil
	}
	if method == "merge_commit" {
		paths, truncated, conflicted, err := service.trialMerge(ctx, repositoryPath, answer.Target.OID, answer.Source.OID, &answer.Leftover)
		if err != nil {
			return err
		}
		if conflicted {
			answer.Status, answer.ConflictPaths, answer.ConflictPathsTruncated = MergeabilityConflict, paths, truncated
			answer.Message = "The source and target branches have merge conflicts."
			return nil
		}
	}
	answer.Status, answer.Method = MergeabilityClean, method
	return nil
}

// trialMerge runs the three-way merge of sourceOID into targetOID that a merge
// commit records, and reports whether it conflicts and which paths do, at
// most MaximumConflictPaths. Git writes the merged trees and files into a
// temporary object directory that borrows the repository's objects and is
// removed afterwards, so the repository gains no object. A failed removal is
// reported in leftover.
func (service *Service) trialMerge(ctx context.Context, repositoryPath, targetOID, sourceOID string, leftover *error) (paths []string, truncated, conflicted bool, err error) {
	objects, err := filepath.Abs(filepath.Join(repositoryPath, "objects"))
	if err != nil {
		return nil, false, false, &Problem{Code: "repository_unavailable", Message: "The repository objects could not be located.", Cause: err}
	}
	scratch, err := os.MkdirTemp(service.Repositories.Git.TempDir, "mergeability-")
	if err != nil {
		return nil, false, false, &Problem{Code: "repository_unavailable", Message: "A temporary folder for the merge check could not be created.", Cause: err}
	}
	defer func() {
		if removeErr := os.RemoveAll(scratch); removeErr != nil {
			*leftover = removeErr
		}
	}()
	environment := []string{"GIT_OBJECT_DIRECTORY=" + scratch, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + quotedObjectDirectory(objects)}
	result, err := service.Repositories.Git.RunWithEnvironment(ctx, repositoryPath, nil, environment,
		"--git-dir", ".", "merge-tree", "--write-tree", "--name-only", "-z", "--no-messages", targetOID, sourceOID)
	if err != nil {
		if code, ok := gitexec.ExitCode(err); !ok || code != 1 {
			return nil, false, false, &Problem{Code: "repository_unavailable", Message: "Git could not calculate the merge.", Cause: err}
		}
		conflicted = true
	}
	// The output is the merged tree ID and then, for a conflict, each
	// conflicting path once, all ending in NUL. Text after the last NUL is
	// empty, or a path cut by the output limit.
	fields := strings.Split(string(result.Stdout), "\x00")
	if len(fields) < 2 || !validOID(fields[0]) {
		return nil, false, false, NewProblem("repository_integrity_error", "Git returned an invalid merge result.")
	}
	if !conflicted {
		return nil, false, false, nil
	}
	listed := fields[1 : len(fields)-1]
	truncated = fields[len(fields)-1] != "" || len(listed) > MaximumConflictPaths
	if len(listed) > MaximumConflictPaths {
		listed = listed[:MaximumConflictPaths]
	}
	return listed, truncated, true, nil
}

// quotedObjectDirectory writes path as one entry of
// GIT_ALTERNATE_OBJECT_DIRECTORIES. Git reads a quoted entry C-style, so a
// path holding the list separator or a backslash stays one path.
func quotedObjectDirectory(path string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(path) + `"`
}
