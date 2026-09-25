package server

import (
	"context"
	"net/http"
	"strings"

	"owngit/internal/pullrequest"
	"owngit/internal/repository"
)

// maximumDiffResponse bounds an encoded diff response. A variable so tests
// can lower it.
var maximumDiffResponse = maximumAPIResponse

// pullRequestDiffQueryAllowed accepts the optional pinned pair of a pull
// request diff: source_oid and target_oid, each at most once.
func pullRequestDiffQueryAllowed(request *http.Request) bool {
	if request.Method != http.MethodGet {
		return false
	}
	if _, _, operation, ok := parsePullRequestAPIRoute(request.URL.Path); !ok || operation != "diff" {
		return false
	}
	for key, values := range request.URL.Query() {
		if (key != "source_oid" && key != "target_oid") || len(values) != 1 {
			return false
		}
	}
	return true
}

// pullRequestDiff reads what pull request number changes between the pair
// that pinned names, or its current pair when pinned is empty. The pair is
// resolved once and diffed by object ID, so a branch that moves during the
// read cannot change what the response says it diffed.
func (app *App) pullRequestDiff(ctx context.Context, repositoryID string, number int64, pinned pullrequest.RevisionInput) (*pullrequest.Diff, error) {
	revisions, err := app.PullRequests.DiffRevisions(ctx, repositoryID, number, pinned)
	if err != nil {
		return nil, err
	}
	comparison, err := app.Repositories.Compare(ctx, repositoryID, revisions.Target.OID, revisions.Source.OID)
	if err != nil {
		return nil, &pullrequest.Problem{Code: "repository_unavailable", Message: "The pull request changes could not be read.", Cause: err}
	}
	diff := diffFromComparison(revisions, comparison)
	diff.Fit(maximumDiffResponse)
	return diff, nil
}

func diffFromComparison(revisions pullrequest.DiffRevisions, comparison repository.Comparison) *pullrequest.Diff {
	diff := &pullrequest.Diff{
		OK: true, Repository: revisions.Repository, Number: revisions.Number, State: revisions.State,
		Source: pullrequest.DiffRevision{Branch: revisions.Source.Branch, OID: revisions.Source.OID},
		Target: pullrequest.DiffRevision{Branch: revisions.Target.Branch, OID: revisions.Target.OID},
		Moved:  revisions.Moved,
		Files:  []pullrequest.DiffFile{},
	}
	if revisions.Moved {
		diff.Current = &pullrequest.DiffCurrent{Source: revisions.CurrentSource, Target: revisions.CurrentTarget}
	}
	switch {
	case comparison.Bases == 0:
		diff.Unavailable = "no_merge_base"
		return diff
	case comparison.Bases > 1:
		diff.Unavailable = "multiple_merge_bases"
		return diff
	}
	diff.MergeBase = comparison.Base
	for _, file := range comparison.Files {
		diff.Files = append(diff.Files, pullrequest.DiffFile{
			Path: file.Path, Status: file.Status, Additions: file.Additions, Deletions: file.Deletions, Binary: file.Binary,
		})
	}
	diff.Patch = comparison.Patch
	diff.Incomplete = comparison.FilesTruncated
	diff.Truncated = comparison.PatchTruncated || comparison.FilesTruncated
	if diff.Truncated {
		// The last file of a cut patch may be partial, so it is left out.
		diff.Patch = diff.Patch[:patchSectionStart(diff.Patch, len(diff.Patch))]
		diff.Reason = "output_limit"
		if comparison.TimedOut {
			diff.Reason = "time_limit"
		}
	}
	return diff
}

// patchSectionStart returns where the last file section that starts before
// end begins, or 0 when none does. A patch read without rename detection
// starts every file with a "diff --git " line, and no other line starts with
// those bytes, since content lines start with a space, "+", "-" or "\".
func patchSectionStart(patch string, end int) int {
	return strings.LastIndex(patch[:end], "\ndiff --git ") + 1
}
