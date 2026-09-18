package pullrequest

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"owngit/internal/repository"
	"owngit/internal/state"
)

type Service struct {
	Store         *state.Store
	Repositories  *repository.Manager
	Now           func() time.Time
	CompleteMerge func(context.Context, state.PullRequestMergeIntent, time.Time) error
}

func (service *Service) Create(ctx context.Context, input CreateInput) (*View, error) {
	if err := repository.ValidateID(input.Repository); err != nil {
		return nil, NewProblem("invalid_repository", "The repository identifier is invalid.")
	}
	title := strings.TrimSpace(input.Title)
	if !validLabel(title, 500) {
		return nil, NewProblem("invalid_title", "The pull request title must contain 1 to 500 characters and no line breaks.")
	}
	source, err := service.normalizeBranch(ctx, input.SourceBranch)
	if err != nil {
		return nil, err
	}
	target, err := service.normalizeBranch(ctx, input.TargetBranch)
	if err != nil {
		return nil, err
	}
	if source == target {
		return nil, NewProblem("same_branch", "The source and target branches must be different.")
	}
	var reviewStatus string
	switch input.ReviewChoice {
	case "request":
		reviewStatus = state.ReviewPending
	case "skip":
		reviewStatus = state.ReviewSkipped
	default:
		return nil, NewProblem("invalid_review_choice", "Review must be explicitly requested or skipped.")
	}
	repositoryPath, err := service.repositoryPath(ctx, input.Repository)
	if err != nil {
		return nil, err
	}
	lock := service.Repositories.Locks.For(input.Repository)
	lock.Lock()
	defer lock.Unlock()
	sourceHead, err := service.resolveBranch(ctx, repositoryPath, source)
	if err != nil {
		return nil, err
	}
	targetHead, err := service.resolveBranch(ctx, repositoryPath, target)
	if err != nil {
		return nil, err
	}
	if err := requireCommitHead("source", sourceHead); err != nil {
		return nil, err
	}
	if err := requireCommitHead("target", targetHead); err != nil {
		return nil, err
	}
	return service.createForHeadsLocked(ctx, input.Repository, title, source, target, reviewStatus, repositoryPath, sourceHead, targetHead)
}

func (service *Service) createForHeadsLocked(ctx context.Context, repositoryID, title, source, target, reviewStatus, repositoryPath string, sourceHead, targetHead branchHead) (*View, error) {
	now := service.now()
	record, err := service.Store.BeginPullRequestCreation(ctx, repositoryID, title, source, target, sourceHead.OID, targetHead.OID, reviewStatus, now)
	if err != nil {
		return nil, &Problem{Code: "state_unavailable", Message: "The provisional pull request metadata could not be saved.", Cause: err}
	}
	if err := service.ensureRevisionRefs(ctx, repositoryPath, record, sourceHead.OID, targetHead.OID); err != nil {
		reconciled, activated, reconcileErr := service.reconcileProvisionalCreationLocked(ctx, repositoryPath, record)
		if reconcileErr != nil {
			return nil, reconcileErr
		}
		if !activated {
			return nil, err
		}
		return service.viewForHeads(ctx, repositoryPath, reconciled, sourceHead, targetHead)
	}
	record, err = service.Store.ActivatePullRequestCreation(ctx, repositoryID, record.Number, service.now())
	if err != nil {
		return nil, &Problem{Code: "pull_request_creation_reconciliation_pending", Message: "The pull request revision was retained, but its visible state still needs reconciliation.", Cause: err}
	}
	return service.viewForHeads(ctx, repositoryPath, record, sourceHead, targetHead)
}

func (service *Service) reconcileProvisionalCreationLocked(ctx context.Context, repositoryPath string, record state.PullRequest) (state.PullRequest, bool, error) {
	revisions, err := service.Store.PullRequestRevisionsFor(ctx, record.RepositoryID, record.Number)
	if err != nil {
		return state.PullRequest{}, false, &Problem{Code: "state_unavailable", Message: "The provisional pull request revision could not be read.", Cause: err}
	}
	if len(revisions) != 1 {
		return state.PullRequest{}, false, NewProblem("repository_integrity_error", "The provisional pull request does not have exactly one retained revision.")
	}
	revision := revisions[0]
	sourceRef, targetRef := RevisionRefNames(record.Number, revision.SourceOID, revision.TargetOID)
	sourceOID, sourceExists, err := service.readRef(ctx, repositoryPath, sourceRef)
	if err != nil {
		return state.PullRequest{}, false, NewProblem("pull_request_creation_reconciliation_pending", "The provisional pull request source revision could not be read safely.")
	}
	targetOID, targetExists, err := service.readRef(ctx, repositoryPath, targetRef)
	if err != nil {
		return state.PullRequest{}, false, NewProblem("pull_request_creation_reconciliation_pending", "The provisional pull request target revision could not be read safely.")
	}
	if !sourceExists && !targetExists {
		if err := service.Store.DeletePullRequestCreation(ctx, record.RepositoryID, record.Number); err != nil {
			return state.PullRequest{}, false, &Problem{Code: "state_unavailable", Message: "The failed pull request creation could not be discarded.", Cause: err}
		}
		return state.PullRequest{}, false, nil
	}
	if !sourceExists || !targetExists || sourceOID != revision.SourceOID || targetOID != revision.TargetOID {
		return state.PullRequest{}, false, NewProblem("repository_integrity_error", "The provisional pull request revision refs are incomplete or have unexpected values.")
	}
	activated, err := service.Store.ActivatePullRequestCreation(ctx, record.RepositoryID, record.Number, service.now())
	if err != nil {
		return state.PullRequest{}, false, &Problem{Code: "pull_request_creation_reconciliation_pending", Message: "The retained pull request creation could not be made visible.", Cause: err}
	}
	return activated, true, nil
}

func (service *Service) List(ctx context.Context, repositoryID string) ([]*View, error) {
	repositoryPath, err := service.repositoryPath(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	lock := service.Repositories.Locks.For(repositoryID)
	lock.RLock()
	defer lock.RUnlock()
	records, err := service.Store.PullRequests(ctx, repositoryID)
	if err != nil {
		return nil, &Problem{Code: "state_unavailable", Message: "Pull request metadata could not be read.", Cause: err}
	}
	if len(records) > MaximumListResults {
		return nil, NewProblem("result_too_large", "The pull request list exceeds the supported response limit. Use show with a pull request number.")
	}
	views := make([]*View, 0, len(records))
	for _, record := range records {
		view, err := service.readViewLocked(ctx, repositoryPath, record)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (service *Service) Show(ctx context.Context, repositoryID string, number int64) (*View, error) {
	if number <= 0 {
		return nil, NewProblem("invalid_pull_request_number", "The pull request number must be positive.")
	}
	repositoryPath, err := service.repositoryPath(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	lock := service.Repositories.Locks.For(repositoryID)
	lock.RLock()
	defer lock.RUnlock()
	record, err := service.requirePullRequest(ctx, repositoryID, number)
	if err != nil {
		return nil, err
	}
	return service.readViewLocked(ctx, repositoryPath, record)
}

func (service *Service) RequestReview(ctx context.Context, repositoryID string, number int64, input RevisionInput) (*View, error) {
	return service.recordReview(ctx, repositoryID, number, input.SourceOID, input.TargetOID, state.ReviewPending, "")
}

func (service *Service) SkipReview(ctx context.Context, repositoryID string, number int64, input RevisionInput) (*View, error) {
	return service.recordReview(ctx, repositoryID, number, input.SourceOID, input.TargetOID, state.ReviewSkipped, "")
}

func (service *Service) SubmitReview(ctx context.Context, repositoryID string, number int64, input ReviewSubmitInput) (*View, error) {
	status := ""
	switch input.Decision {
	case state.ReviewApproved:
		status = state.ReviewApproved
	case state.ReviewChangesRequested:
		status = state.ReviewChangesRequested
	default:
		return nil, NewProblem("invalid_review_decision", "A submitted review must be approved or changes_requested.")
	}
	reviewer := strings.TrimSpace(input.ReviewerLabel)
	if !validLabel(reviewer, 200) {
		return nil, NewProblem("invalid_reviewer_label", "The supplied reviewer label must contain 1 to 200 characters and no line breaks.")
	}
	return service.recordReview(ctx, repositoryID, number, input.SourceOID, input.TargetOID, status, reviewer)
}

func (service *Service) recordReview(ctx context.Context, repositoryID string, number int64, sourceOID, targetOID, status, reviewer string) (*View, error) {
	if err := validateExpectedRevision(sourceOID, targetOID); err != nil {
		return nil, err
	}
	repositoryPath, err := service.repositoryPath(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	lock := service.Repositories.Locks.For(repositoryID)
	lock.Lock()
	defer lock.Unlock()
	record, err := service.requirePullRequest(ctx, repositoryID, number)
	if err != nil {
		return nil, err
	}
	if record.Status != state.PullRequestOpen {
		return nil, NewProblem("pull_request_not_open", "The pull request is already merged.")
	}
	sourceHead, targetHead, err := service.currentHeads(ctx, repositoryPath, record)
	if err != nil {
		return nil, err
	}
	if sourceHead.OID != sourceOID || targetHead.OID != targetOID {
		return nil, staleRevisionProblem(sourceHead.OID, targetHead.OID)
	}
	if err := service.bindRevision(ctx, repositoryPath, record, sourceOID, targetOID); err != nil {
		return nil, err
	}
	review := state.PullRequestReview{
		RepositoryID: repositoryID, PullRequestNumber: number, SourceOID: sourceOID, TargetOID: targetOID,
		Status: status, ReviewerLabel: reviewer, CreatedAt: service.now(),
	}
	switch status {
	case state.ReviewPending:
		review.Provenance = state.ReviewProvenanceRequest
	case state.ReviewSkipped:
		review.Provenance = state.ReviewProvenanceSkip
	default:
		review.Provenance = state.ReviewProvenanceExternalTool
	}
	if _, err := service.Store.AppendPullRequestReview(ctx, review); err != nil {
		return nil, &Problem{Code: "state_unavailable", Message: "The review decision could not be saved.", Cause: err}
	}
	record, err = service.requirePullRequest(ctx, repositoryID, number)
	if err != nil {
		return nil, err
	}
	return service.viewForHeads(ctx, repositoryPath, record, sourceHead, targetHead)
}

func (service *Service) Merge(ctx context.Context, repositoryID string, number int64, input RevisionInput) (*View, error) {
	if err := validateExpectedRevision(input.SourceOID, input.TargetOID); err != nil {
		return nil, err
	}
	repositoryPath, err := service.repositoryPath(ctx, repositoryID)
	if err != nil {
		return nil, err
	}
	lock := service.Repositories.Locks.For(repositoryID)
	lock.Lock()
	defer lock.Unlock()
	record, err := service.requirePullRequest(ctx, repositoryID, number)
	if err != nil {
		return nil, err
	}
	if record.Status == state.PullRequestMerged {
		if record.MergeSourceOID != input.SourceOID || record.MergeTargetOID != input.TargetOID {
			return nil, NewProblem("stale_revision", "The pull request was merged for a different source and target revision.")
		}
		intent, ok, err := service.Store.PullRequestMergeIntent(ctx, repositoryID, number, input.SourceOID, input.TargetOID)
		if err != nil {
			return nil, &Problem{Code: "state_unavailable", Message: "The merge receipt metadata could not be read.", Cause: err}
		}
		if !ok || intent.ResultOID != record.MergeOID {
			return nil, NewProblem("repository_integrity_error", "The merged pull request does not have matching merge intent metadata.")
		}
		published, err := service.validateReceipt(ctx, repositoryPath, intent)
		if err != nil {
			return nil, err
		}
		if !published {
			return nil, NewProblem("repository_integrity_error", "The merged pull request receipt is missing.")
		}
		return service.readViewLocked(ctx, repositoryPath, record)
	}

	sourceHead, targetHead, err := service.currentHeads(ctx, repositoryPath, record)
	if err != nil {
		return nil, err
	}
	if sourceHead.OID != input.SourceOID || targetHead.OID != input.TargetOID {
		return nil, staleRevisionProblem(sourceHead.OID, targetHead.OID)
	}
	if err := service.bindRevision(ctx, repositoryPath, record, input.SourceOID, input.TargetOID); err != nil {
		return nil, err
	}
	view, err := service.viewForHeads(ctx, repositoryPath, record, sourceHead, targetHead)
	if err != nil {
		return nil, err
	}
	if !view.MergeEligibility.Eligible {
		return nil, &Problem{Code: "merge_blocked", Message: "The pull request is not eligible to merge.", Details: view.MergeEligibility}
	}

	intent, err := service.Store.BeginPullRequestMerge(ctx, state.PullRequestMergeIntent{
		RepositoryID: repositoryID, PullRequestNumber: number, SourceOID: input.SourceOID, TargetOID: input.TargetOID,
		ReceiptRef: MergeReceiptRef(number), CreatedAt: service.now(),
	})
	if err != nil {
		return nil, &Problem{Code: "state_unavailable", Message: "The durable merge intent could not be saved.", Cause: err}
	}
	if intent.Status == state.MergeIntentReady {
		published, reconcileErr := service.reconcileIntentLocked(ctx, repositoryPath, intent)
		if reconcileErr != nil {
			return nil, reconcileErr
		}
		if published {
			record, err = service.requirePullRequest(ctx, repositoryID, number)
			if err != nil {
				return nil, err
			}
			return service.readViewLocked(ctx, repositoryPath, record)
		}
	}
	if err := service.requireMergeCapability(ctx); err != nil {
		return nil, err
	}
	if intent.Status == state.MergeIntentPreparing {
		intent, err = service.planMerge(ctx, repositoryPath, intent)
		if err != nil {
			return nil, err
		}
		if err := service.ensurePlannedMergeTree(ctx, repositoryPath, record, intent, true); err != nil {
			return nil, err
		}
		intent, err = service.Store.UpdatePullRequestMergeIntent(ctx, intent)
		if err != nil {
			return nil, &Problem{Code: "state_unavailable", Message: "The merge plan could not be saved.", Cause: err}
		}
	}
	switch intent.Status {
	case state.MergeIntentPlanned:
		intent.ResultOID, err = service.ensureMergeResult(ctx, repositoryPath, record, intent, true)
		if err != nil {
			return nil, err
		}
		intent.Status = state.MergeIntentReady
		intent.UpdatedAt = service.now()
		intent, err = service.Store.UpdatePullRequestMergeIntent(ctx, intent)
		if err != nil {
			return nil, &Problem{Code: "state_unavailable", Message: "The merge result could not be saved before publication.", Cause: err}
		}
	case state.MergeIntentReady:
		if _, err := service.ensureMergeResult(ctx, repositoryPath, record, intent, true); err != nil {
			return nil, err
		}
	default:
		return nil, NewProblem("repository_integrity_error", "The merge intent is in an unexpected state.")
	}

	publishErr := service.publishMerge(ctx, repositoryPath, record, intent)
	published, reconcileErr := service.reconcileIntentLocked(ctx, repositoryPath, intent)
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	if !published {
		currentSource, currentTarget, readErr := service.currentHeads(ctx, repositoryPath, record)
		if readErr != nil {
			return nil, NewProblem("merge_reconciliation_pending", "The merge result could not be read back safely. Retry after the repository is available.")
		}
		if currentSource.OID != intent.SourceOID || currentTarget.OID != intent.TargetOID {
			return nil, staleRevisionProblem(currentSource.OID, currentTarget.OID)
		}
		return nil, &Problem{Code: "git_update_failed", Message: "Git did not publish the merge transaction.", Cause: publishErr}
	}
	record, err = service.requirePullRequest(ctx, repositoryID, number)
	if err != nil {
		return nil, err
	}
	return service.readViewLocked(ctx, repositoryPath, record)
}

func (service *Service) ReconcileAll(ctx context.Context) error {
	repositories, err := service.Store.Repositories(ctx)
	if err != nil {
		return fmt.Errorf("read repositories for pull request reconciliation: %w", err)
	}
	for _, stored := range repositories {
		provisional, err := service.Store.ProvisionalPullRequests(ctx, stored.ID)
		if err != nil {
			return fmt.Errorf("read provisional pull requests for %s: %w", stored.ID, err)
		}
		revisions, err := service.Store.PullRequestRevisions(ctx, stored.ID)
		if err != nil {
			return fmt.Errorf("read pull request revisions for %s: %w", stored.ID, err)
		}
		if len(provisional) == 0 && len(revisions) == 0 {
			continue
		}
		repositoryPath, err := service.repositoryPath(ctx, stored.ID)
		if err != nil {
			return err
		}
		lock := service.Repositories.Locks.For(stored.ID)
		lock.Lock()
		for _, record := range provisional {
			if _, _, err := service.reconcileProvisionalCreationLocked(ctx, repositoryPath, record); err != nil {
				lock.Unlock()
				return fmt.Errorf("reconcile pull request creation %s#%d: %w", stored.ID, record.Number, err)
			}
		}
		for _, revision := range revisions {
			if err := service.ensureStoredRevisionRefs(ctx, repositoryPath, revision); err != nil {
				lock.Unlock()
				return fmt.Errorf("repair pull request revision %s#%d: %w", stored.ID, revision.PullRequestNumber, err)
			}
		}
		lock.Unlock()
	}

	intents, err := service.Store.PullRequestMergeIntents(ctx, true)
	if err != nil {
		return fmt.Errorf("read incomplete pull request merges: %w", err)
	}
	for _, intent := range intents {
		if intent.Status == state.MergeIntentPreparing {
			continue
		}
		_, ok, err := service.Store.PullRequest(ctx, intent.RepositoryID, intent.PullRequestNumber)
		if err != nil {
			return fmt.Errorf("read pull request %s#%d for merge reconciliation: %w", intent.RepositoryID, intent.PullRequestNumber, err)
		}
		if !ok {
			return fmt.Errorf("merge intent refers to missing pull request %s#%d", intent.RepositoryID, intent.PullRequestNumber)
		}
		repositoryPath, err := service.repositoryPath(ctx, intent.RepositoryID)
		if err != nil {
			return err
		}
		lock := service.Repositories.Locks.For(intent.RepositoryID)
		lock.Lock()
		if intent.Status == state.MergeIntentReady {
			_, err = service.reconcileIntentLocked(ctx, repositoryPath, intent)
		} else if intent.Status == state.MergeIntentPlanned {
			err = service.validateProtectedMergeObjects(ctx, repositoryPath, intent)
		} else {
			err = NewProblem("repository_integrity_error", "The merge intent is in an unexpected state.")
		}
		lock.Unlock()
		if err != nil {
			return fmt.Errorf("reconcile pull request %s#%d: %w", intent.RepositoryID, intent.PullRequestNumber, err)
		}
	}
	return nil
}

func (service *Service) reconcileIntentLocked(ctx context.Context, repositoryPath string, intent state.PullRequestMergeIntent) (bool, error) {
	published, err := service.validateReceipt(ctx, repositoryPath, intent)
	if err != nil || !published {
		return published, err
	}
	completedAt := service.now()
	complete := service.CompleteMerge
	if complete == nil {
		complete = service.Store.CompletePullRequestMerge
	}
	if err := complete(ctx, intent, completedAt); err != nil {
		return true, &Problem{Code: "merge_reconciliation_pending", Message: "Git published the merge, but its durable state still needs reconciliation. Retry the same merge command.", Cause: err}
	}
	return true, nil
}

func (service *Service) validateReceipt(ctx context.Context, repositoryPath string, intent state.PullRequestMergeIntent) (bool, error) {
	if err := service.validateProtectedMergeObjects(ctx, repositoryPath, intent); err != nil {
		return false, err
	}
	receiptOID, exists, err := service.readRef(ctx, repositoryPath, intent.ReceiptRef)
	if err != nil {
		return false, NewProblem("merge_reconciliation_pending", "The merge receipt could not be read safely. Retry after the repository is available.")
	}
	if !exists {
		return false, nil
	}
	if receiptOID != intent.ResultOID {
		return false, NewProblem("repository_integrity_error", "The protected merge receipt does not match the durable merge intent.")
	}
	return true, nil
}

// readViewLocked builds a view from the current heads without retaining a new
// revision pair. Callers that record a decision or a merge bind the pair
// explicitly, so passive reads stay read-only.
func (service *Service) readViewLocked(ctx context.Context, repositoryPath string, record state.PullRequest) (*View, error) {
	if record.Status == state.PullRequestMerged {
		source := branchHead{Branch: record.SourceBranch, OID: record.MergeSourceOID, Status: "commit"}
		target := branchHead{Branch: record.TargetBranch, OID: record.MergeTargetOID, Status: "commit"}
		return service.viewForHeads(ctx, repositoryPath, record, source, target)
	}
	source, target, err := service.readHeads(ctx, repositoryPath, record)
	if err != nil {
		return nil, err
	}
	return service.viewForHeads(ctx, repositoryPath, record, source, target)
}

func (service *Service) viewForHeads(ctx context.Context, repositoryPath string, record state.PullRequest, source, target branchHead) (*View, error) {
	view := &View{
		Repository: record.RepositoryID, Number: record.Number, Title: record.Title, State: record.Status,
		Source:    Revision{Branch: record.SourceBranch, OID: source.OID, Status: source.Status},
		Target:    Revision{Branch: record.TargetBranch, OID: target.OID, Status: target.Status},
		Checks:    Checks{Status: "not_configured", Blocking: false},
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	if source.Status == "commit" && target.Status == "commit" {
		review, exists, err := service.Store.PullRequestReviewForRevision(ctx, record.RepositoryID, record.Number, source.OID, target.OID)
		if err != nil {
			return nil, &Problem{Code: "state_unavailable", Message: "The review state could not be read.", Cause: err}
		}
		if exists {
			submitted := review.CreatedAt
			view.Review = Review{
				Status: review.Status, SourceOID: review.SourceOID, TargetOID: review.TargetOID,
				ReviewerLabel: review.ReviewerLabel, Provenance: review.Provenance,
				Independent: false, ExecutedChecks: false, SubmittedAt: &submitted,
			}
		} else {
			view.Review = Review{Status: "decision_required", Independent: false, ExecutedChecks: false}
		}
	} else {
		view.Review = Review{Status: "decision_required", Independent: false, ExecutedChecks: false}
	}
	view.MergeEligibility = evaluateEligibility(record.Status, source, target, view.Review)
	if record.Status == state.PullRequestMerged {
		intent, ok, err := service.Store.PullRequestMergeIntent(ctx, record.RepositoryID, record.Number, record.MergeSourceOID, record.MergeTargetOID)
		if err != nil {
			return nil, &Problem{Code: "state_unavailable", Message: "The merge metadata could not be read.", Cause: err}
		}
		if !ok || intent.ResultOID != record.MergeOID || record.MergedAt == nil {
			return nil, NewProblem("repository_integrity_error", "The merged pull request metadata is incomplete.")
		}
		view.Merge = &MergeResult{Mode: intent.Mode, OID: record.MergeOID, ReceiptRef: record.MergeReceipt, MergedAt: *record.MergedAt}
	}
	_ = repositoryPath
	return view, nil
}

func evaluateEligibility(requestStatus string, source, target branchHead, review Review) Eligibility {
	eligibility := Eligibility{}
	if requestStatus == state.PullRequestMerged {
		eligibility.Blockers = append(eligibility.Blockers, Blocker{Code: "already_merged", Message: "The pull request is already merged."})
		return eligibility
	}
	for _, head := range []struct {
		name string
		head branchHead
	}{{"source", source}, {"target", target}} {
		switch head.head.Status {
		case "missing":
			eligibility.Blockers = append(eligibility.Blockers, Blocker{Code: head.name + "_branch_missing", Message: "The " + head.name + " branch no longer exists."})
		case "not_commit":
			eligibility.Blockers = append(eligibility.Blockers, Blocker{Code: head.name + "_not_commit", Message: "The " + head.name + " branch does not point to a commit."})
		}
	}
	if source.Status == "commit" && target.Status == "commit" {
		switch review.Status {
		case state.ReviewApproved, state.ReviewSkipped:
		case state.ReviewPending:
			eligibility.Blockers = append(eligibility.Blockers, Blocker{Code: "review_pending", Message: "The requested review has no current result."})
		case state.ReviewChangesRequested:
			eligibility.Blockers = append(eligibility.Blockers, Blocker{Code: "changes_requested", Message: "The current review requests changes. Submit a fresh result or explicitly skip review."})
		default:
			eligibility.Blockers = append(eligibility.Blockers, Blocker{Code: "review_decision_required", Message: "The source or target revision changed. Request, submit, or skip review for the current revisions."})
		}
	}
	eligibility.Eligible = len(eligibility.Blockers) == 0
	return eligibility
}

func (service *Service) readHeads(ctx context.Context, repositoryPath string, record state.PullRequest) (branchHead, branchHead, error) {
	source, err := service.resolveBranch(ctx, repositoryPath, record.SourceBranch)
	if err != nil {
		return branchHead{}, branchHead{}, err
	}
	target, err := service.resolveBranch(ctx, repositoryPath, record.TargetBranch)
	if err != nil {
		return branchHead{}, branchHead{}, err
	}
	return source, target, nil
}

func (service *Service) currentHeads(ctx context.Context, repositoryPath string, record state.PullRequest) (branchHead, branchHead, error) {
	source, target, err := service.readHeads(ctx, repositoryPath, record)
	if err != nil {
		return branchHead{}, branchHead{}, err
	}
	if err := requireCommitHead("source", source); err != nil {
		return branchHead{}, branchHead{}, err
	}
	if err := requireCommitHead("target", target); err != nil {
		return branchHead{}, branchHead{}, err
	}
	return source, target, nil
}

func (service *Service) bindRevision(ctx context.Context, repositoryPath string, record state.PullRequest, sourceOID, targetOID string) error {
	if err := service.ensureRevisionRefs(ctx, repositoryPath, record, sourceOID, targetOID); err != nil {
		return err
	}
	if err := service.Store.RecordPullRequestRevision(ctx, state.PullRequestRevision{
		RepositoryID: record.RepositoryID, PullRequestNumber: record.Number, SourceOID: sourceOID, TargetOID: targetOID, RecordedAt: service.now(),
	}); err != nil {
		return &Problem{Code: "state_unavailable", Message: "The pull request revision binding could not be saved.", Cause: err}
	}
	return nil
}

func (service *Service) requirePullRequest(ctx context.Context, repositoryID string, number int64) (state.PullRequest, error) {
	if number <= 0 {
		return state.PullRequest{}, NewProblem("invalid_pull_request_number", "The pull request number must be positive.")
	}
	record, exists, err := service.Store.PullRequest(ctx, repositoryID, number)
	if err != nil {
		return state.PullRequest{}, &Problem{Code: "state_unavailable", Message: "Pull request metadata could not be read.", Cause: err}
	}
	if !exists {
		return state.PullRequest{}, NewProblem("pull_request_not_found", "The pull request does not exist.")
	}
	return record, nil
}

func (service *Service) repositoryPath(ctx context.Context, repositoryID string) (string, error) {
	if err := repository.ValidateID(repositoryID); err != nil {
		return "", NewProblem("invalid_repository", "The repository identifier is invalid.")
	}
	path, _, exists, err := service.Repositories.ExistingPath(ctx, repositoryID)
	if err != nil {
		return "", &Problem{Code: "repository_unavailable", Message: "The repository storage is unavailable.", Cause: err}
	}
	if !exists {
		return "", NewProblem("repository_not_found", "The repository does not exist.")
	}
	return path, nil
}

func (service *Service) normalizeBranch(ctx context.Context, value string) (string, error) {
	if value != strings.TrimSpace(value) || value == "" || len(value) > 255 || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "", NewProblem("invalid_branch", "The branch name is invalid.")
	}
	if strings.HasPrefix(value, "refs/owngit/") {
		return "", NewProblem("reserved_ref", "The refs/owngit namespace is reserved for OwnGit.")
	}
	if strings.HasPrefix(value, "refs/heads/") {
		value = strings.TrimPrefix(value, "refs/heads/")
	} else if strings.HasPrefix(value, "refs/") {
		return "", NewProblem("invalid_branch", "Pull requests require branch refs under refs/heads.")
	}
	if value == "HEAD" || value == "" {
		return "", NewProblem("invalid_branch", "The branch name is invalid.")
	}
	if _, err := service.Repositories.Git.Run(ctx, "", nil, "check-ref-format", "refs/heads/"+value); err != nil {
		return "", NewProblem("invalid_branch", "The branch name is invalid.")
	}
	return value, nil
}

func requireCommitHead(name string, head branchHead) error {
	switch head.Status {
	case "commit":
		return nil
	case "missing":
		return NewProblem(name+"_branch_missing", "The "+name+" branch does not exist.")
	case "not_commit":
		return NewProblem(name+"_not_commit", "The "+name+" branch does not point to a commit.")
	default:
		return NewProblem("repository_integrity_error", "The "+name+" branch has an unknown state.")
	}
}

func validateExpectedRevision(sourceOID, targetOID string) error {
	if !validOID(sourceOID) || !validOID(targetOID) {
		return NewProblem("invalid_revision", "Exact lowercase source and target commit object IDs are required.")
	}
	return nil
}

func staleRevisionProblem(sourceOID, targetOID string) *Problem {
	return &Problem{
		Code: "stale_revision", Message: "The source or target branch changed. Inspect the pull request and use its current object IDs.",
		Details: map[string]string{"current_source_oid": sourceOID, "current_target_oid": targetOID},
	}
}

func validLabel(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func (service *Service) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}
