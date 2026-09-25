package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

const maximumBrowserDiffFiles = 200

func (app *App) handlePullRequestsGet(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	page := app.pullRequestsPage(request, stored, summary, chrome)
	status := http.StatusOK
	if app.PullRequests == nil {
		page.Unavailable = true
		page.UnavailableReason = webui.MsgErrUnavailable
		status = http.StatusServiceUnavailable
	} else {
		views, err := app.PullRequests.List(request.Context(), stored.ID)
		if err != nil {
			page.Unavailable = true
			page.UnavailableReason = webui.MsgErrUnavailable
			status = browserProblemStatus(err)
		} else {
			for _, view := range views {
				page.Items = append(page.Items, app.pullRequestRow(stored.ID, view))
			}
		}
	}
	app.render(writer, status, page)
}

func (app *App) pullRequestsPage(request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) webui.PullRequestsPage {
	basePage := app.baseRepositoryPage(request, chrome, stored, summary)
	base := basePage.Repo.URL + "/pull-requests"
	page := webui.PullRequestsPage{
		Chrome: chrome,
		Repo:   basePage.Repo,
		Tabs:   repositoryTabs(basePage, webui.RepoTabPullRequests),
	}
	if len(summary.Branches) >= 2 && app.PullRequests != nil {
		page.NewURL = base + "/new"
	}
	return page
}

func (app *App) pullRequestRow(repositoryID string, view *pullrequest.View) webui.PullRequestRow {
	if view == nil {
		return webui.PullRequestRow{}
	}
	return webui.PullRequestRow{
		Number:    view.Number,
		Title:     view.Title,
		URL:       pullRequestURL(repositoryID, view.Number),
		State:     view.State,
		Source:    browserRevision(view.Source),
		Target:    browserRevision(view.Target),
		Checks:    browserCheckEvidence(repositoryID, view.Checks),
		Review:    browserReviewEvidence(view.Review, view.Source, view.Target),
		CreatedAt: view.CreatedAt,
		UpdatedAt: view.UpdatedAt,
	}
}

func (app *App) handleNewPullRequestGet(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	app.renderNewPullRequest(writer, request, stored, summary, chrome,
		request.URL.Query().Get("source"), request.URL.Query().Get("target"), "", webui.ReviewChoiceNone, nil, http.StatusOK)
}

func (app *App) renderNewPullRequest(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, sourceBranch, targetBranch, title, reviewChoice string, notices []webui.Notice, status int) {
	basePage := app.baseRepositoryPage(request, chrome, stored, summary)
	base := basePage.Repo.URL + "/pull-requests"
	page := webui.NewPullRequestPage{
		Chrome:       chrome,
		Repo:         basePage.Repo,
		Tabs:         repositoryTabs(basePage, webui.RepoTabPullRequests),
		SelectURL:    base + "/new",
		SubmitURL:    base,
		CancelURL:    base,
		Title:        title,
		ReviewChoice: reviewChoice,
	}
	if notices != nil {
		page.Chrome.Notices = notices
	}

	if len(summary.Branches) >= 2 {
		for _, branch := range summary.Branches {
			page.Branches = append(page.Branches, webui.RefOption{Name: branch.Name})
		}
	}
	if sourceBranch == "" && targetBranch == "" {
		page.Source.Branch, page.Target.Branch = defaultPullRequestBranches(summary)
		app.render(writer, status, page)
		return
	}

	page.Observed = true
	page.Source = observedBranch(summary, sourceBranch)
	page.Target = observedBranch(summary, targetBranch)
	if sourceBranch == "" || targetBranch == "" {
		page.ChangesUnavailable = true
		page.ChangesReason = webui.MsgPRInvalidBranch
		if status == http.StatusOK {
			status = http.StatusUnprocessableEntity
		}
	} else if sourceBranch == targetBranch {
		page.Chrome.Notices = append(page.Chrome.Notices, webui.Error("source_branch", webui.MsgPRSameBranch))
		if status == http.StatusOK {
			status = http.StatusUnprocessableEntity
		}
	} else if !page.Source.Resolved() || !page.Target.Resolved() {
		page.ChangesUnavailable = true
	} else {
		files, truncated, err := app.comparePullRequestRevisions(request.Context(), stored.ID, page.Source.OID, page.Target.OID)
		if err != nil {
			page.ChangesUnavailable = true
			page.ChangesReason = webui.MsgErrUnavailable
			if status == http.StatusOK {
				status = http.StatusServiceUnavailable
			}
		} else {
			page.Changes = files
			page.DiffTruncated = truncated
		}
	}
	app.render(writer, status, page)
}

func (app *App) handleCreatePullRequest(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	if !parseForm(writer, request) {
		return
	}
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}

	input := pullrequest.CreateInput{
		Repository:   stored.ID,
		Title:        postValue(request, "title"),
		SourceBranch: postValue(request, "source_branch"),
		TargetBranch: postValue(request, "target_branch"),
		ReviewChoice: postValue(request, "review"),
		SourceOID:    postValue(request, "source_oid"),
		TargetOID:    postValue(request, "target_oid"),
	}
	if !validOID(input.SourceOID) || !validOID(input.TargetOID) {
		app.renderNewPullRequest(writer, request, stored, summary, chrome, input.SourceBranch, input.TargetBranch, input.Title, input.ReviewChoice,
			[]webui.Notice{webui.Error("", webui.MsgPRStale)}, http.StatusUnprocessableEntity)
		return
	}
	if app.PullRequests == nil {
		app.renderNewPullRequest(writer, request, stored, summary, chrome, input.SourceBranch, input.TargetBranch, input.Title, input.ReviewChoice,
			[]webui.Notice{webui.Error("", webui.MsgPRFailed)}, http.StatusServiceUnavailable)
		return
	}
	created, err := app.PullRequests.Create(request.Context(), input)
	if err != nil {
		notice, status := browserPullRequestProblem(err, "")
		if existing, ok := pullrequest.AsProblem(err).Details.(pullrequest.ExistingPullRequest); ok {
			notice = notice.WithLink("#"+strconv.FormatInt(existing.Number, 10), pullRequestURL(stored.ID, existing.Number))
		}
		app.renderNewPullRequest(writer, request, stored, summary, chrome, input.SourceBranch, input.TargetBranch, input.Title, input.ReviewChoice,
			[]webui.Notice{notice}, status)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	http.Redirect(writer, request, pullRequestURL(stored.ID, created.Number)+"?notice=pull_request_created", http.StatusSeeOther)
}

func (app *App) handlePullRequestGet(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, number int64) {
	app.renderPullRequest(writer, request, stored, summary, chrome, number, nil, nil, nil, http.StatusOK)
}

func (app *App) handlePullRequestAction(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, number int64, action string) {
	if !parseForm(writer, request) {
		return
	}
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	if app.PullRequests == nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgErrUnavailable, "")
		return
	}
	input := pullrequest.RevisionInput{SourceOID: postValue(request, "source_oid"), TargetOID: postValue(request, "target_oid")}
	var (
		view   *pullrequest.View
		err    error
		notice string
	)
	switch action {
	case "review_request":
		view, err = app.PullRequests.RequestReview(request.Context(), stored.ID, number, input)
		notice = "review_requested"
	case "review_skip":
		view, err = app.PullRequests.SkipReview(request.Context(), stored.ID, number, input)
		notice = "review_skipped"
	case "merge":
		view, err = app.PullRequests.Merge(request.Context(), stored.ID, number, input)
		notice = "pull_request_merged"
		if err == nil && view.Merge != nil && view.Merge.Mode == webui.MergeModeUpToDate {
			notice = "pull_request_up_to_date"
		}
	case "close":
		view, err = app.PullRequests.Close(request.Context(), stored.ID, number)
		notice = "pull_request_closed"
	case "reopen":
		view, err = app.PullRequests.Reopen(request.Context(), stored.ID, number)
		notice = "pull_request_reopened"
	default:
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, request.URL.Path)
		return
	}
	if err != nil {
		problemNotice, status := browserPullRequestProblem(err, action)
		if existing, ok := pullrequest.AsProblem(err).Details.(pullrequest.ExistingPullRequest); ok {
			problemNotice = problemNotice.WithLink("#"+strconv.FormatInt(existing.Number, 10), pullRequestURL(stored.ID, existing.Number))
		}
		var blockers []webui.MergeBlocker
		if action == "merge" {
			blockers = browserMergeProblemBlockers(err)
		}
		app.renderPullRequest(writer, request, stored, summary, chrome, number, nil, []webui.Notice{problemNotice}, blockers, status)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	http.Redirect(writer, request, pullRequestURL(stored.ID, view.Number)+"?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}

func (app *App) renderPullRequest(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, number int64, view *pullrequest.View, notices []webui.Notice, extraBlockers []webui.MergeBlocker, status int) {
	if app.PullRequests == nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgErrUnavailable, "")
		return
	}
	if view == nil {
		var err error
		view, err = app.PullRequests.Show(request.Context(), stored.ID, number)
		if err != nil {
			problem := pullrequest.AsProblem(err)
			if problem.Code == "pull_request_not_found" || problem.Code == "invalid_pull_request_number" {
				app.renderError(writer, request, http.StatusNotFound, webui.MsgPRNotFound, "")
				return
			}
			app.renderError(writer, request, browserProblemStatus(err), webui.MsgPRFailed, "")
			return
		}
	}
	if view.Repository != stored.ID {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgPRNotFound, "")
		return
	}

	basePage := app.baseRepositoryPage(request, chrome, stored, summary)
	self := pullRequestURL(stored.ID, view.Number)
	page := webui.PullRequestPage{
		Chrome:    chrome,
		Repo:      basePage.Repo,
		Tabs:      repositoryTabs(basePage, webui.RepoTabPullRequests),
		Number:    view.Number,
		Title:     view.Title,
		State:     view.State,
		Source:    browserRevision(view.Source),
		Target:    browserRevision(view.Target),
		Checks:    browserCheckEvidence(stored.ID, view.Checks),
		Review:    browserReviewEvidence(view.Review, view.Source, view.Target),
		Merge:     browserMergeAvailability(view.MergeEligibility),
		SelfURL:   self,
		ListURL:   basePage.PullRequestsURL,
		TasksURL:  basePage.TasksURL,
		CreatedAt: view.CreatedAt,
		UpdatedAt: view.UpdatedAt,
	}
	if notices != nil {
		page.Chrome.Notices = notices
	} else {
		page.Chrome.Notices = pullRequestNotices(request.URL.Query().Get("notice"), view)
	}
	page.Merge.Blockers = append(page.Merge.Blockers, extraBlockers...)
	if len(extraBlockers) != 0 {
		page.Merge.Eligible = false
	}
	if view.Merge != nil {
		page.Merged = &webui.MergeRecord{
			Mode:       view.Merge.Mode,
			OID:        view.Merge.OID,
			ShortOID:   shortOID(view.Merge.OID),
			ReceiptRef: view.Merge.ReceiptRef,
			MergedAt:   view.Merge.MergedAt,
		}
	}
	if view.State == state.PullRequestOpen && page.Source.Resolved() && page.Target.Resolved() {
		page.ReviewRequestURL = self + "/review/request"
		page.ReviewSkipURL = self + "/review/skip"
		page.MergeURL = self + "/merge"
	}
	// Closing needs no branch, so it stays available after the source branch
	// was deleted, which is a common reason to close.
	switch view.State {
	case state.PullRequestOpen:
		page.CloseURL = self + "/close"
	case state.PullRequestClosed:
		page.ReopenURL = self + "/reopen"
	}
	if !page.Source.Resolved() || !page.Target.Resolved() {
		page.ChangesUnavailable = true
	} else {
		files, truncated, err := app.comparePullRequestRevisions(request.Context(), stored.ID, page.Source.OID, page.Target.OID)
		if err != nil {
			page.ChangesUnavailable = true
			page.ChangesReason = webui.MsgErrUnavailable
			if status == http.StatusOK {
				status = http.StatusServiceUnavailable
			}
		} else {
			page.Changes = files
			page.DiffTruncated = truncated
		}
	}
	app.render(writer, status, page)
}

// pullRequestNotices shows the result of an action this page redirected from,
// but only while the pull request's current state still confirms it. The
// notice key comes from the address, so anyone can write it into a link; a
// key the state contradicts, or one meant for another page, shows nothing.
func pullRequestNotices(key string, view *pullrequest.View) []webui.Notice {
	open := view.State == state.PullRequestOpen
	merged := view.State == state.PullRequestMerged && view.Merge != nil
	var confirmed bool
	var code webui.MessageCode
	switch key {
	case "pull_request_created":
		confirmed, code = open, webui.MsgPRCreated
	case "review_requested":
		confirmed, code = open && view.Review.Status == state.ReviewPending, webui.MsgPRReviewAsked
	case "review_skipped":
		confirmed, code = open && view.Review.Status == state.ReviewSkipped, webui.MsgPRReviewSkipped
	case "pull_request_merged":
		confirmed, code = merged && view.Merge.Mode != webui.MergeModeUpToDate, webui.MsgPRMerged
	case "pull_request_up_to_date":
		confirmed, code = merged && view.Merge.Mode == webui.MergeModeUpToDate, webui.MsgPRUpToDate
	case "pull_request_closed":
		confirmed, code = view.State == state.PullRequestClosed, webui.MsgPRClosedDone
	case "pull_request_reopened":
		confirmed, code = open, webui.MsgPRReopenedDone
	}
	if !confirmed {
		return nil
	}
	return []webui.Notice{webui.Success(code)}
}

func browserRevision(revision pullrequest.Revision) webui.RevisionState {
	return webui.RevisionState{
		Branch:   revision.Branch,
		OID:      revision.OID,
		ShortOID: shortOID(revision.OID),
		Status:   revision.Status,
	}
}

func browserCheckEvidence(repositoryID string, checks pullrequest.Checks) webui.CheckEvidence {
	evidence := webui.CheckEvidence{
		Status:               checks.Status,
		Configured:           checks.Configured,
		Passed:               checks.Passed,
		TestedCommit:         checks.TestedCommit,
		Advisory:             checks.Advisory,
		Stale:                checks.Stale,
		WorktreeState:        checks.WorktreeState,
		RevisionOID:          checks.RevisionOID,
		RevisionShortOID:     shortOID(checks.RevisionOID),
		ConfigurationVersion: checks.ConfigurationVersion,
		AttemptID:            checks.AttemptID,
		AttemptShortID:       shortOpaqueID(checks.AttemptID),
		Summary:              checks.Summary,
		LogStatus:            webui.LogStatusOf(checks.LogStatus),
		OutputTruncated:      checks.LogTruncated,
		Protection:           browserProtection(checks.JobID, checks.Protection, checks.ExecutionScope),
		LogError:             checks.LogError,
		ReadFailure:          browserEvidenceReadFailure(checks.ReadFailure, webui.MsgCheckRecordUnreadable),
		CleanupFailed:        checks.CleanupFailed,
	}
	if checks.FinishedAt != nil {
		evidence.FinishedAt = *checks.FinishedAt
	}
	if checks.RegisteredAt != nil {
		evidence.RegisteredAt = *checks.RegisteredAt
	}
	if checks.LogExpiresAt != nil {
		evidence.LogExpiresAt = *checks.LogExpiresAt
	}
	evidence.CredentialProvenance = browserProvenance(checks.JobID, checks.CredentialID, checks.ExecutionScope)
	if checks.TaskID != "" {
		evidence.AttemptURL = tasksURL(repositoryID, checks.TaskID)
	}
	return evidence
}

func browserReviewEvidence(review pullrequest.Review, source, target pullrequest.Revision) webui.ReviewEvidence {
	bound := review.SourceOID == source.OID && review.TargetOID == target.OID && review.SourceOID != "" && review.TargetOID != ""
	evidence := webui.ReviewEvidence{
		Status:                 review.Status,
		Provenance:             review.Provenance,
		ReviewerLabel:          review.ReviewerLabel,
		Independent:            review.Independent,
		ExecutedChecks:         review.ExecutedChecks,
		SourceOID:              review.SourceOID,
		TargetOID:              review.TargetOID,
		ShortSourceOID:         shortOID(review.SourceOID),
		BoundToCurrentRevision: bound,
		ReadFailure:            browserEvidenceReadFailure(review.ReadFailure, webui.MsgReviewRecordUnreadable),
		Detail:                 review.Detail,
	}
	if review.SubmittedAt != nil {
		evidence.SubmittedAt = *review.SubmittedAt
	}
	return evidence
}

func browserEvidenceReadFailure(failure *pullrequest.ReadFailure, fallback webui.MessageCode) *webui.EvidenceReadFailure {
	if failure == nil {
		return nil
	}
	message := fallback
	switch failure.Code {
	case pullrequest.ReadFailureCheckConfiguration:
		message = webui.MsgCheckConfigurationUnreadable
	case pullrequest.ReadFailureCheckEvidence:
		message = webui.MsgCheckRecordUnreadable
	case pullrequest.ReadFailureReviewEvidence:
		message = webui.MsgReviewRecordUnreadable
	}
	return &webui.EvidenceReadFailure{Code: failure.Code, Message: message}
}

func browserMergeAvailability(eligibility pullrequest.Eligibility) webui.MergeAvailability {
	result := webui.MergeAvailability{Eligible: eligibility.Eligible}
	for _, blocker := range eligibility.Blockers {
		result.Blockers = append(result.Blockers, browserMergeBlocker(blocker.Code, blocker.Message))
	}
	return result
}

func browserMergeBlocker(code, detail string) webui.MergeBlocker {
	blocker := webui.MergeBlocker{Code: code}
	switch code {
	case "already_merged", "source_branch_missing", "target_branch_missing", "source_not_commit", "target_not_commit", "merge_conflict", "stale_revision", "pull_request_not_open", "git_update_failed":
		// The renderer has localized text for these stable codes. Repeating the
		// service's English sentence as detail would make a Korean page bilingual.
	default:
		blocker.Detail = detail
	}
	return blocker
}

func browserMergeProblemBlockers(err error) []webui.MergeBlocker {
	problem := pullrequest.AsProblem(err)
	if eligibility, ok := problem.Details.(pullrequest.Eligibility); ok {
		return browserMergeAvailability(eligibility).Blockers
	}
	switch problem.Code {
	case "merge_conflict", "stale_revision", "pull_request_not_open", "git_update_failed":
		return []webui.MergeBlocker{browserMergeBlocker(problem.Code, problem.Message)}
	default:
		return nil
	}
}

func browserPullRequestProblem(err error, action string) (webui.Notice, int) {
	problem := pullrequest.AsProblem(err)
	field := ""
	code := webui.MsgPRFailed
	switch problem.Code {
	case "invalid_title":
		field, code = "title", webui.MsgPRInvalidTitle
	case "invalid_branch", "reserved_ref":
		field, code = "source_branch", webui.MsgPRInvalidBranch
	case "source_branch_missing", "source_not_commit":
		field, code = "source_branch", webui.MsgPRInvalidBranch
		if action != "" {
			// The pull request page has no branch field, so the notice is page
			// level there and says what happened to the branch.
			field, code = "", webui.MsgMergeBlockedSourceGone
			if problem.Code == "source_not_commit" {
				code = webui.MsgMergeBlockedSourceKind
			}
		}
	case "target_branch_missing", "target_not_commit":
		field, code = "target_branch", webui.MsgPRInvalidBranch
		if action != "" {
			field, code = "", webui.MsgMergeBlockedTargetGone
			if problem.Code == "target_not_commit" {
				code = webui.MsgMergeBlockedTargetKind
			}
		}
	case "pull_request_exists":
		code = webui.MsgPRAlreadyOpen
	case "pull_request_merged":
		code = webui.MsgPRMergedFixed
	case "same_branch":
		field, code = "source_branch", webui.MsgPRSameBranch
	case "invalid_review_choice":
		field, code = "review", webui.MsgPRFailed
	case "invalid_revision", "stale_revision":
		code = webui.MsgPRStale
	case "merge_conflict", "merge_blocked", "git_update_failed":
		field, code = "merge", webui.MsgPRMergeBlocked
	case "pull_request_not_found", "invalid_pull_request_number":
		code = webui.MsgPRNotFound
	case "pull_request_not_open":
		code = webui.MsgPRNotOpen
	case "merge_reconciliation_pending", "pull_request_creation_reconciliation_pending":
		code = webui.MsgPRReconciling
	}
	if action == "merge" && field == "" && (problem.Code == "stale_revision" || problem.Code == "invalid_revision") {
		field = "merge"
	}
	return webui.Error(field, code), browserProblemStatus(err)
}

func browserProblemStatus(err error) int {
	status := apiStatus(pullrequest.AsProblem(err).Code)
	if status == http.StatusInternalServerError {
		return http.StatusServiceUnavailable
	}
	return status
}

func observedBranch(summary repository.Summary, name string) webui.RevisionState {
	revision := webui.RevisionState{Branch: name, Status: webui.RevisionMissing}
	for _, branch := range summary.Branches {
		if branch.Name != name {
			continue
		}
		revision.OID = branch.OID
		revision.ShortOID = shortOID(branch.OID)
		revision.Status = webui.RevisionNotCommit
		if branch.Type == "commit" && validOID(branch.OID) {
			revision.Status = webui.RevisionCommit
		}
		return revision
	}
	return revision
}

func defaultPullRequestBranches(summary repository.Summary) (string, string) {
	if len(summary.Branches) == 0 {
		return "", ""
	}
	target := summary.DefaultBranch
	if target == "" {
		target = summary.Branches[0].Name
	}
	source := summary.Branches[0].Name
	for _, branch := range summary.Branches {
		if branch.Name != target {
			source = branch.Name
			break
		}
	}
	return source, target
}

func repositoryTabs(page webui.RepositoryPage, active webui.RepoTab) webui.RepoTabs {
	return webui.RepoTabs{
		OverviewURL:     page.OverviewURL,
		CodeURL:         page.CodeURL,
		CommitsURL:      page.CommitsURL,
		PullRequestsURL: page.PullRequestsURL,
		TasksURL:        page.TasksURL,
		ImportsURL:      page.ImportsURL,
		SettingsURL:     page.SettingsURL,
		DeleteURL:       page.DeleteURL,
		AdminLocked:     !page.Chrome.Viewer.AdminConfirmed,
		Active:          active,
	}
}

func pullRequestURL(repositoryID string, number int64) string {
	return "/repositories/" + url.PathEscape(repositoryID) + "/pull-requests/" + strconv.FormatInt(number, 10)
}

func tasksURL(repositoryID, taskID string) string {
	base := "/repositories/" + url.PathEscape(repositoryID) + "/tasks"
	if taskID == "" {
		return base
	}
	return base + "?task=" + url.QueryEscape(taskID)
}

func shortOpaqueID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// browserProtection names where a recorded attempt actually ran.
//
// The distinction that matters is manual against automatic. A manual helper
// run happens in the operator's own environment with their permissions; an
// automatic job is the server's own work under a saved policy, on the server
// host, in a container it started, or on an external runner. Those are
// different machines and different authorities, so they never share a
// sentence.
//
// The job link is the authority for that division, because it is the recorded
// fact rather than an inference: an attempt carries a job identifier only when
// OwnGit admitted and ran the work itself. The execution scope and protection
// are read afterwards, and only to choose which automatic wording applies.
// They cannot promote an unlinked attempt to automatic, and an automatic
// attempt whose protection was never established is reported as unknown rather
// than described as the operator's own run.
func browserProtection(jobID, protection, executionScope string) string {
	if jobID == "" {
		// No job admitted this run. The inherited scope with no established
		// protection is exactly what a manual helper submission records.
		// Anything else was not recorded as a manual run either, so it claims
		// nothing instead of borrowing an automatic sentence.
		if executionScope == state.ExecutionScopeInherited && protection == state.ProtectionUnknown {
			return webui.ProtectionInherited
		}
		if protection == state.ProtectionUnknown {
			return webui.ProtectionUnknown
		}
		return ""
	}
	// An admitted job ran on the server. Which of the three automatic sentences
	// applies is decided by the executor-derived scope together with the
	// protection the job actually established.
	switch executionScope {
	case state.ExecutionScopeContainer:
		if protection == state.ProtectionContainer {
			return webui.ProtectionAutomaticContainer
		}
	case state.ExecutionScopeExternalRunner:
		if protection == state.ProtectionRunnerReported {
			return webui.ProtectionRunnerReported
		}
	case state.ExecutionScopeInherited:
		if protection == state.ProtectionHost {
			return webui.ProtectionAutomaticHost
		}
	}
	// The store permits an automatic job to record no protection at all. That
	// is a missing fact about an automatic run, never evidence of a manual one.
	return webui.ProtectionUnknown
}

// browserProvenance names how an attempt reached OwnGit.
//
// A helper and a runner both authenticate, but they are not the same origin,
// so they never share a sentence. The job link decides whether OwnGit ran the
// work itself, and the executor-derived scope separates the server's own run
// from work an external runner claimed. An attempt with no credential states
// nothing.
func browserProvenance(jobID, credentialID, executionScope string) string {
	if credentialID == "" {
		return webui.ProvenanceUnstated
	}
	if jobID == "" {
		return webui.ProvenanceAuthenticatedHelper
	}
	if executionScope == state.ExecutionScopeExternalRunner {
		return webui.ProvenanceRunnerClaimed
	}
	return webui.ProvenanceAutomaticJob
}

func (app *App) comparePullRequestRevisions(ctx context.Context, repositoryID, sourceOID, targetOID string) ([]webui.DiffFile, bool, error) {
	if !validOID(sourceOID) || !validOID(targetOID) {
		return nil, false, errors.New("invalid pull request revision")
	}
	repositoryPath, _, exists, err := app.Repositories.ExistingPath(ctx, repositoryID)
	if err != nil || !exists {
		if err == nil {
			err = errors.New("repository not found")
		}
		return nil, false, err
	}
	lock := app.Repositories.Locks.For(repositoryID)
	lock.RLock()
	defer lock.RUnlock()

	statusResult, err := app.Repositories.Git.Run(ctx, repositoryPath, nil,
		"--git-dir", ".", "diff", "--name-status", "--no-renames", "-z", targetOID, sourceOID)
	if err != nil {
		return nil, false, fmt.Errorf("read pull request changes: %w", err)
	}
	files, err := parseBrowserChangedFiles(statusResult.Stdout)
	if err != nil {
		return nil, false, err
	}
	numResult, err := app.Repositories.Git.Run(ctx, repositoryPath, nil,
		"--git-dir", ".", "diff", "--numstat", "--no-renames", "-z", targetOID, sourceOID)
	if err != nil {
		return nil, false, fmt.Errorf("read pull request change sizes: %w", err)
	}
	counts, err := parseBrowserNumstat(numResult.Stdout)
	if err != nil {
		return nil, false, err
	}
	for index := range files {
		if count, ok := counts[files[index].Path]; ok {
			files[index].Additions = count.additions
			files[index].Deletions = count.deletions
			files[index].Binary = count.binary
		}
	}

	truncated := false
	for index := range files {
		if index >= maximumBrowserDiffFiles {
			truncated = true
			files[index].NotLoaded = !files[index].Binary
			continue
		}
		if files[index].Binary {
			continue
		}
		result, runErr := app.Repositories.Git.RunWithOutputLimit(ctx, repositoryPath, nil, 256<<10,
			"--git-dir", ".", "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=3",
			targetOID, sourceOID, "--", ":(top,literal)"+files[index].Path)
		if runErr != nil {
			var limitErr *gitexec.LimitError
			if !errors.As(runErr, &limitErr) {
				return nil, false, fmt.Errorf("read pull request patch for %q: %w", files[index].Path, runErr)
			}
			truncated = true
		}
		files[index].Hunks = parsePatch(string(result.Stdout))
	}
	return files, truncated, nil
}

func parseBrowserChangedFiles(output []byte) ([]webui.DiffFile, error) {
	tokens := bytes.Split(output, []byte{0})
	var files []webui.DiffFile
	for index := 0; index < len(tokens) && len(tokens[index]) != 0; index += 2 {
		if index+1 >= len(tokens) || len(tokens[index]) != 1 || len(tokens[index+1]) == 0 {
			return nil, errors.New("Git returned malformed pull request changes")
		}
		status := browserChangeStatus(tokens[index][0])
		if status == "" {
			return nil, errors.New("Git returned an unknown pull request change status")
		}
		files = append(files, webui.DiffFile{Path: string(tokens[index+1]), Status: status})
	}
	return files, nil
}

type browserLineCount struct {
	additions int
	deletions int
	binary    bool
}

func parseBrowserNumstat(output []byte) (map[string]browserLineCount, error) {
	counts := make(map[string]browserLineCount)
	for _, token := range bytes.Split(output, []byte{0}) {
		if len(token) == 0 {
			continue
		}
		fields := bytes.SplitN(token, []byte{'\t'}, 3)
		if len(fields) != 3 || len(fields[2]) == 0 {
			return nil, errors.New("Git returned malformed pull request change sizes")
		}
		count := browserLineCount{}
		if string(fields[0]) == "-" && string(fields[1]) == "-" {
			count.binary = true
		} else {
			var err error
			count.additions, err = strconv.Atoi(string(fields[0]))
			if err != nil || count.additions < 0 {
				return nil, errors.New("Git returned invalid pull request additions")
			}
			count.deletions, err = strconv.Atoi(string(fields[1]))
			if err != nil || count.deletions < 0 {
				return nil, errors.New("Git returned invalid pull request deletions")
			}
		}
		counts[string(fields[2])] = count
	}
	return counts, nil
}

func browserChangeStatus(code byte) string {
	switch code {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'M', 'T':
		return "modified"
	default:
		return ""
	}
}

func parsePullRequestNumber(value string) (int64, bool) {
	number, err := strconv.ParseInt(value, 10, 64)
	return number, err == nil && number > 0 && strings.TrimSpace(value) == value
}
