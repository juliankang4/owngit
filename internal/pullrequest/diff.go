package pullrequest

import (
	"context"

	"owngit/internal/state"
)

// DiffRevisions is the exact revision pair a pull request diff reads, with
// the pull request's current revisions for comparison.
type DiffRevisions struct {
	Repository string
	Number     int64
	State      string
	// Source and Target are the pair to diff. Status is always "commit".
	Source Revision
	Target Revision
	// CurrentSource and CurrentTarget are what the pull request shows now:
	// the branch heads, or the merged pair once it is merged.
	CurrentSource Revision
	CurrentTarget Revision
	// Moved is set when a pinned pair is not the current pair.
	Moved bool
}

// DiffRevisions resolves the revision pair a diff of pull request number
// reads. Without a pinned pair it is the current pair: the branch heads read
// once, or the merged pair of a merged pull request, and both branches must
// point to commits. A pinned pair is accepted only when it is the current
// pair or a pair recorded for this pull request, whose commits OwnGit keeps,
// so the diff never reads arbitrary objects. A pinned pair is diffed even
// when the branches moved; Moved and the current revisions say so.
func (service *Service) DiffRevisions(ctx context.Context, repositoryID string, number int64, pinned RevisionInput) (DiffRevisions, error) {
	pinning := pinned.SourceOID != "" || pinned.TargetOID != ""
	if pinning {
		if err := validateExpectedRevision(pinned.SourceOID, pinned.TargetOID); err != nil {
			return DiffRevisions{}, err
		}
	}
	repositoryPath, err := service.repositoryPath(ctx, repositoryID)
	if err != nil {
		return DiffRevisions{}, err
	}
	lock := service.Repositories.Locks.For(repositoryID)
	if err := lockForRequest(ctx, lock.RLockContext); err != nil {
		return DiffRevisions{}, err
	}
	defer lock.RUnlock()
	record, err := service.requirePullRequest(ctx, repositoryID, number)
	if err != nil {
		return DiffRevisions{}, err
	}
	var source, target branchHead
	if record.Status == state.PullRequestMerged {
		source = branchHead{Branch: record.SourceBranch, OID: record.MergeSourceOID, Status: "commit"}
		target = branchHead{Branch: record.TargetBranch, OID: record.MergeTargetOID, Status: "commit"}
	} else if source, target, err = service.readHeads(ctx, repositoryPath, record); err != nil {
		return DiffRevisions{}, err
	}
	result := DiffRevisions{
		Repository: record.RepositoryID, Number: record.Number, State: record.Status,
		CurrentSource: Revision{Branch: record.SourceBranch, OID: source.OID, Status: source.Status},
		CurrentTarget: Revision{Branch: record.TargetBranch, OID: target.OID, Status: target.Status},
	}
	current := source.Status == "commit" && target.Status == "commit"
	if !pinning {
		if err := requireCommitHead("source", source); err != nil {
			return DiffRevisions{}, err
		}
		if err := requireCommitHead("target", target); err != nil {
			return DiffRevisions{}, err
		}
		pinned = RevisionInput{SourceOID: source.OID, TargetOID: target.OID}
	} else if !current || pinned.SourceOID != source.OID || pinned.TargetOID != target.OID {
		recorded, err := service.Store.HasPullRequestRevision(ctx, repositoryID, number, pinned.SourceOID, pinned.TargetOID)
		if err != nil {
			return DiffRevisions{}, &Problem{Code: "state_unavailable", Message: "Pull request revision history could not be read.", Cause: err}
		}
		if !recorded {
			return DiffRevisions{}, NewProblem("revision_not_recorded", "The source and target object IDs are neither the pull request's current revisions nor a pair recorded for it.")
		}
		result.Moved = true
	}
	result.Source = Revision{Branch: record.SourceBranch, OID: pinned.SourceOID, Status: "commit"}
	result.Target = Revision{Branch: record.TargetBranch, OID: pinned.TargetOID, Status: "commit"}
	return result, nil
}
