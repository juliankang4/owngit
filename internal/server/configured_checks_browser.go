package server

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"owngit/internal/auth"
	"owngit/internal/checkapi"
	"owngit/internal/checkworkflow"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// Browser screens for repository-configured checks.
//
// Two screens live here. One reads and changes the execution policy, turns
// execution on or off, and lists what the recorded jobs did. The other manages
// the repository-scoped tokens an external runner authenticates with.
//
// Both are administrator only, and that is decided by the route rather than by
// which controls a page draws. Every mutation additionally verifies the
// current administrator password, because a remembered session must not be
// able to grant execution authority or mint a token by itself.
//
// Nothing new is decided here. The policy, consent, job and credential rules
// are the backend's, and this file only carries browser input into them and
// their outcomes back out.

const (
	// maximumBrowserJobs bounds the job list one page renders.
	maximumBrowserJobs = 50
	// maximumBrowserLogBytes bounds the recorded output one page renders. It
	// is a display bound, separate from the backend's own capture limit, and
	// the page says when it applied.
	maximumBrowserLogBytes = 64 << 10
)

func configuredChecksURL(repositoryID string) string {
	return "/repositories/" + url.PathEscape(repositoryID) + "/configured-checks"
}

// configuredCheckJobURL is the GET address of one opened job, built from the
// screen's own address.
//
// The identifier can be whatever a request carried, so it is query-escaped
// here rather than trusted. An unknown or malformed job renders as not found
// at this same address, which is the screen the reader is actually on.
func configuredCheckJobURL(selfURL, jobID string) string {
	return selfURL + "?job=" + url.QueryEscape(jobID)
}

func runnerTokensURL(repositoryID string) string {
	return "/repositories/" + url.PathEscape(repositoryID) + "/runner-tokens"
}

// handleConfiguredChecks serves the execution policy screen and its forms.
func (app *App) handleConfiguredChecks(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	writer.Header().Set("Cache-Control", "no-store")
	adminSession, ok := app.requireBrowserAdmin(writer, request)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionRepository, stored.ID, adminSession.CSRF)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	// A GET is a plain read. It never admits a job, never reserves a
	// revision, and never grants execution authority.
	if request.Method == http.MethodGet {
		app.renderConfiguredChecks(writer, request, stored, summary, chrome, configuredChecksState{}, http.StatusOK)
		return
	}
	if !parseForm(writer, request) {
		return
	}
	action := postValue(request, "action")
	if !constantEqual(adminSession.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	// Only the policy form owns the editor. A failed password on an enable,
	// disable, cancel or rerun must not repaint the editor with whatever that
	// unrelated form happened to post, which for those forms is nothing at all
	// and would silently blank every saved setting on screen.
	var form *webui.CheckPolicyForm
	if action == webui.ActionSaveCheckPolicy {
		submitted := submittedPolicyForm(request)
		form = &submitted
	}
	if err := app.Auth.VerifyCredential(request.Context(), "admin", postValue(request, "admin_password"), request.RemoteAddr); err != nil {
		code, status := webui.MsgAdminFailed, http.StatusUnauthorized
		if errors.Is(err, auth.ErrRateLimited) {
			code, status = webui.MsgAdminLocked, http.StatusTooManyRequests
		}
		app.renderConfiguredChecks(writer, request, stored, summary, chrome, configuredChecksState{
			action: action, form: form,
			notices: []webui.Notice{webui.Error("admin_password", code)},
		}, status)
		return
	}

	switch action {
	case webui.ActionSaveCheckPolicy:
		app.saveCheckPolicy(writer, request, stored, summary, chrome, *form)
	case webui.ActionEnableChecks, webui.ActionDisableChecks:
		app.changeCheckConsent(writer, request, stored, summary, chrome, action)
	case webui.ActionCancelCheckJob, webui.ActionRerunCheckJob:
		app.changeCheckJob(writer, request, stored, summary, chrome, action)
	default:
		app.renderConfiguredChecks(writer, request, stored, summary, chrome, configuredChecksState{
			action: action, notices: []webui.Notice{webui.Error("action", webui.MsgSettingsUnknownAct)},
		}, http.StatusBadRequest)
	}
}

// configuredChecksState is what one response carries beyond the stored facts:
// which form was submitted, what it submitted, and what came of it.
type configuredChecksState struct {
	action  string
	form    *webui.CheckPolicyForm
	notices []webui.Notice
}

func (app *App) saveCheckPolicy(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, form webui.CheckPolicyForm) {
	input, notices := policyInputFrom(stored.ID, form)
	if len(notices) != 0 {
		app.renderConfiguredChecks(writer, request, stored, summary, chrome, configuredChecksState{
			action: webui.ActionSaveCheckPolicy, form: &form, notices: notices,
		}, http.StatusUnprocessableEntity)
		return
	}
	saved, err := app.Store.SetCheckPolicy(request.Context(), input, app.now())
	if err != nil {
		app.renderConfiguredChecks(writer, request, stored, summary, chrome, configuredChecksState{
			action: webui.ActionSaveCheckPolicy, form: &form,
			notices: policySaveNotices(err),
		}, policySaveStatus(err))
		return
	}
	app.wakeChecks(stored.ID)
	// Saving a changed policy clears consent; resubmitting an unchanged one
	// leaves it exactly as it was. The result has to say which happened, or an
	// operator with running checks is told execution is off.
	notice := "check_policy_saved"
	if saved.ConsentActive {
		notice = "check_policy_saved_enabled"
	}
	// A saved policy is a durable change, so the result is a redirect: a
	// reload re-reads it instead of re-submitting the form.
	app.noticeRedirect(writer, request, configuredChecksURL(stored.ID)+"?notice="+notice, http.StatusSeeOther)
}

func (app *App) changeCheckConsent(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, action string) {
	var err error
	if action == webui.ActionEnableChecks {
		// The approval carries the identity the screen rendered, and the store
		// compares it inside the transaction that writes the consent. Reading
		// the policy here and granting afterwards would leave a window in
		// which another writer replaces it, and the approval would land on a
		// generation nobody read.
		// Whatever the form quoted is passed through as it arrived. A missing,
		// partial or unparsable identity is not repaired here and not judged
		// here: GrantCheckConsentFor requires a whole identity and answers
		// ErrCheckPolicyStale, which becomes a 409 below. Keeping one authority
		// means a future change to that rule cannot leave this screen quietly
		// applying the old one.
		expected := state.ExpectedCheckPolicy{Digest: postValue(request, "policy_digest")}
		if version, parseErr := strconv.ParseInt(postValue(request, "policy_version"), 10, 64); parseErr == nil {
			expected.Version = version
		}
		_, err = app.Store.GrantCheckConsentFor(request.Context(), stored.ID, expected, app.now())
	} else {
		_, err = app.Store.RevokeCheckConsent(request.Context(), stored.ID, app.now())
	}
	if err != nil {
		code, status := webui.MsgCCFailed, http.StatusServiceUnavailable
		switch {
		case errors.Is(err, state.ErrCheckPolicyStale):
			code, status = webui.MsgCCPolicyStale, http.StatusConflict
		case errors.Is(err, state.ErrCheckPolicyMissing):
			code, status = webui.MsgCCPolicyMissing, http.StatusConflict
		case errors.Is(err, state.ErrInvalidCheckPolicy):
			code, status = webui.MsgCCPolicyRefused, http.StatusConflict
		}
		app.renderConfiguredChecks(writer, request, stored, summary, chrome, configuredChecksState{
			action: action, notices: []webui.Notice{webui.Error("", code)},
		}, status)
		return
	}
	app.wakeChecks(stored.ID)
	notice := "checks_enabled"
	if action == webui.ActionDisableChecks {
		notice = "checks_disabled"
	}
	app.noticeRedirect(writer, request, configuredChecksURL(stored.ID)+"?notice="+notice, http.StatusSeeOther)
}

func (app *App) changeCheckJob(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, action string) {
	jobID := postValue(request, "job_id")
	if !validAttemptID(jobID) {
		app.renderConfiguredChecks(writer, request, stored, summary, chrome, configuredChecksState{
			action: action, notices: []webui.Notice{webui.Error("", webui.MsgCCJobMissing)},
		}, http.StatusUnprocessableEntity)
		return
	}
	notice := "check_job_cancelled"
	// The rerun result names the job the operator should now be watching,
	// whether it is the new one or the outstanding one their request joined.
	follow := ""
	var err error
	if action == webui.ActionCancelCheckJob {
		// The returned job is the authority on what the request achieved. A
		// cancel that arrives after the work finished is accepted and recorded
		// as intent, but it reverses nothing, so the result must not be
		// reported as a cancellation.
		var cancelled state.CheckJob
		cancelled, err = app.Store.CancelCheckJob(request.Context(), stored.ID, jobID, app.now())
		if err == nil && terminalBrowserJob(cancelled.Status) && cancelled.Status != state.CheckJobCancelled {
			// The job had already reached a result of its own. Saying "a
			// cancellation was recorded" here would suggest work was stopped
			// and leave the reader expecting the outcome to change.
			notice = "check_job_already_finished"
		}
	} else {
		var rerun state.CheckJob
		var deduped bool
		rerun, deduped, err = app.Store.RerunCheckJob(request.Context(), stored.ID, jobID, app.now())
		notice = "check_job_rerun"
		if err == nil {
			follow = rerun.ID
			if deduped {
				// An outstanding rerun already carries the request, so nothing
				// new was queued. Saying so is more honest than a second
				// success, and the identity points at the existing job.
				notice = "check_job_rerun_existing"
			}
		}
	}
	if err != nil {
		code, status := webui.MsgCCFailed, http.StatusServiceUnavailable
		switch {
		case errors.Is(err, state.ErrCheckJobNotFound):
			code, status = webui.MsgCCJobMissing, http.StatusNotFound
		case errors.Is(err, state.ErrCheckJobState), errors.Is(err, state.ErrCheckConsentRequired),
			errors.Is(err, state.ErrCheckQueueFull), errors.Is(err, state.ErrCheckEventNotAllowed):
			code, status = webui.MsgCCJobRefused, http.StatusConflict
		}
		app.renderConfiguredChecks(writer, request, stored, summary, chrome, configuredChecksState{
			action: action, notices: []webui.Notice{webui.Error("", code)},
		}, status)
		return
	}
	app.wakeChecks(stored.ID)
	// A rerun opens the job that now carries the request, so the operator is
	// not left reading the job they reran. Otherwise stay where they were.
	opened := request.URL.Query().Get("job")
	if follow != "" && opened != "" {
		opened = follow
	}
	self := configuredChecksURL(stored.ID)
	target := self + "?notice=" + notice
	if opened != "" {
		target = configuredCheckJobURL(self, opened) + "&notice=" + notice
	}
	app.noticeRedirect(writer, request, target, http.StatusSeeOther)
}

func (app *App) renderConfiguredChecks(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, result configuredChecksState, status int) {
	basePage := app.baseRepositoryPage(request, chrome, stored, summary)
	self := configuredChecksURL(stored.ID)
	page := webui.ConfiguredChecksPage{
		Chrome:          chrome,
		Repo:            basePage.Repo,
		Tabs:            repositoryTabs(basePage, webui.RepoTabChecks),
		SelfURL:         self,
		SubmitURL:       self,
		TasksURL:        basePage.TasksURL,
		RunnerTokensURL: runnerTokensURL(stored.ID),
		Runtime:         browserRuntimeView(app.checkRuntimeStatus()),
		PendingAction:   result.action,
	}
	if result.notices != nil {
		page.Chrome.Notices = result.notices
	}

	policy, exists, err := app.Store.CheckPolicy(request.Context(), stored.ID)
	if err != nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgCCFailed, "")
		return
	}
	page.Policy = browserCheckPolicy(policy, exists)
	// A refused submission re-renders exactly what was typed. Otherwise the
	// fields show the stored policy, so values the owner is not changing keep
	// their saved settings.
	if result.form != nil {
		page.Form = *result.form
	} else {
		page.Form = policyFormFrom(page.Policy)
	}
	// The accepted ranges and defaults come from the backend on every render,
	// refused or not, so the editor states the same numbers that will judge
	// the next submission.
	page.Form.Ranges = policyFieldRanges()
	page.Form.Defaults = policyFieldDefaults()

	if opened := request.URL.Query().Get("job"); opened != "" {
		page.Detail = app.browserCheckJobDetail(request, stored.ID, opened, self)
		// An opened job is a screen of its own, so its address is the one a
		// language link has to keep. SelfURL is this page's own GET address and
		// is what those links are built from; leaving it at the policy address
		// would switch the language and silently return the reader to the
		// policy screen on the next reload. SubmitURL stays the POST route,
		// which the job forms already extend with the job they act on.
		page.SelfURL = configuredCheckJobURL(self, opened)
		app.render(writer, status, page)
		return
	}

	page.CheckFile = app.browserCheckFile(request, stored.ID, summary, policy, exists)
	if credentials, err := app.Store.CheckRunnerCredentials(request.Context(), stored.ID); err == nil {
		page.RunnerTokensKnown = true
		for _, credential := range credentials {
			if credential.RevokedAt == nil {
				page.ActiveRunnerTokens++
			}
		}
	}

	jobs, err := app.Store.LatestCheckJobs(request.Context(), stored.ID, maximumBrowserJobs+1)
	if err != nil {
		page.JobsUnavailable = true
		app.render(writer, status, page)
		return
	}
	if len(jobs) > maximumBrowserJobs {
		jobs = jobs[:maximumBrowserJobs]
		page.JobsTruncated = true
	}
	for _, job := range jobs {
		page.Jobs = append(page.Jobs, browserCheckJobRow(job, self))
	}
	app.render(writer, status, page)
}

// browserCheckFile reports what the default branch holds at the check file
// path, read and judged exactly as job admission does: the same bounded read
// of the same path, the same file checks, and the same parser.
//
// It answers the owner's "is my file there and does OwnGit accept it?". It is
// only a hint, because every job reads the file from the exact commit it
// checks, and a lookup that fails is reported as unreadable rather than as a
// missing file.
func (app *App) browserCheckFile(request *http.Request, repositoryID string, summary repository.Summary, policy state.CheckPolicy, policyExists bool) webui.CheckFileView {
	view := webui.CheckFileView{Branch: summary.DefaultBranch}
	if summary.Empty || summary.DefaultOID == "" {
		view.State = webui.CheckFileNoCommits
		return view
	}
	metadataLimit := state.DefaultCheckSourceLimits().MetadataLimit
	if policyExists && policy.Execution.Source.MetadataLimit > 0 {
		metadataLimit = policy.Execution.Source.MetadataLimit
	}
	pinned, err := app.Repositories.PinRepository(request.Context(), repositoryID, summary.DefaultOID, summary.DefaultOID)
	if err != nil {
		view.State = webui.CheckFileUnreadable
		return view
	}
	blob, err := pinned.ReadBlob(request.Context(), repository.PinnedHead, checkworkflow.Path, 0, metadataLimit,
		checkworkflow.MaximumBytes+1, checkworkflow.MaximumBytes+1)
	switch {
	case errors.Is(err, repository.ErrPinnedPathNotFound):
		view.State = webui.CheckFileMissing
		return view
	case errors.Is(err, repository.ErrPinnedUnsupportedObject), errors.Is(err, repository.ErrPinnedOutputLimit):
		view.State, view.Problem = webui.CheckFileInvalid, err.Error()
		return view
	case err != nil:
		view.State = webui.CheckFileUnreadable
		return view
	}
	if blob.Symlink || (blob.Mode != "100644" && blob.Mode != "100755") || blob.HasMore ||
		blob.Size != int64(len(blob.Content)) || len(blob.Content) > checkworkflow.MaximumBytes {
		view.State, view.Problem = webui.CheckFileInvalid, "the check file must be a regular file of at most 64 KiB"
		return view
	}
	document, err := checkworkflow.Parse(blob.Content)
	if err != nil {
		view.State, view.Problem = webui.CheckFileInvalid, err.Error()
		return view
	}
	view.State = webui.CheckFileFound
	view.Checks = len(document.Checks)
	view.Events = document.EventNames()
	return view
}

// browserCheckJobDetail assembles one opened job.
//
// A record that could not be read is never reported as a record that does not
// exist. "This job is not in this repository" and "the store could not answer"
// lead to opposite conclusions, and the second one must not quietly borrow the
// first one's wording. The same applies below the job: a job that names an
// attempt has a run registered, so a failed attempt read is reported as
// unreadable rather than as "nothing ran".
func (app *App) browserCheckJobDetail(request *http.Request, repositoryID, jobID, selfURL string) *webui.CheckJobDetail {
	detail := &webui.CheckJobDetail{SubmitURL: configuredCheckJobURL(selfURL, jobID), BackURL: selfURL}
	if !validAttemptID(jobID) {
		detail.NotFound = true
		return detail
	}
	job, exists, err := app.Store.CheckJob(request.Context(), repositoryID, jobID)
	if err != nil {
		detail.Unreadable = true
		return detail
	}
	if !exists {
		detail.NotFound = true
		return detail
	}
	detail.Job = browserCheckJobRow(job, selfURL)

	// The commands are the job's captured configuration. Failing to read them
	// does not mean the job defined none.
	configuration, found, err := app.Store.CheckConfiguration(request.Context(), repositoryID, job.ConfigurationVersion)
	switch {
	case err != nil:
		detail.ChecksUnreadable = true
	case !found:
		detail.ChecksMissing = true
	default:
		for _, check := range configuration.Checks {
			detail.Checks = append(detail.Checks, webui.CheckDefinitionLine{Name: check.Name, Command: check.Command})
		}
	}

	if job.AttemptID == "" {
		// No attempt was ever bound to this job, which is the one case that
		// honestly means nothing ran under it.
		return detail
	}
	// From here the job names an attempt, so "no run registered" is false
	// whatever the read returns.
	attempt, found, err := app.Store.CheckAttemptByID(request.Context(), repositoryID, job.AttemptID)
	if err != nil {
		detail.AttemptUnreadable = true
		return detail
	}
	if !found {
		detail.AttemptMissing = true
		return detail
	}
	record := app.browserAttemptRecord(attempt)
	detail.Attempt = &record
	detail.Log = app.browserCheckJobLog(attempt)
	return detail
}

// browserCheckJobLog reads the disposable raw log for one attempt.
//
// The log outlives nothing: it expires, and the durable record stays. Expired,
// absent, and unreadable are three separate answers here, never one blank.
// browserCheckJobLog reports the disposable log for one attempt.
//
// The read itself is the authority on what the log is. CheckLogState only
// describes what the record claims, which can disagree with the file after an
// expiry sweep or a failed write, so the state it returns is used solely for
// the case where no read is attempted at all.
func (app *App) browserCheckJobLog(attempt state.CheckAttempt) webui.CheckJobLogView {
	view := webui.CheckJobLogView{
		Truncated: attempt.LogTruncated,
		Error:     attempt.LogError,
	}
	if attempt.LogExpiresAt != nil {
		view.ExpiresAt = *attempt.LogExpiresAt
	}
	// One call decides everything: it answers missing for an absent or
	// unusable identity, expired past the retention bound, and found only when
	// the content is actually there.
	content, logState, err := app.Store.ReadCheckLog(attempt.LogID, attempt.LogExpiresAt, app.now())
	if err != nil {
		// The log could not be read. That is not the same as "it produced no
		// output" and not the same as "it expired".
		view.Status = webui.LogUnavailable
		return view
	}
	switch logState {
	case state.CheckLogExpired:
		view.Status = webui.LogExpired
		return view
	case state.CheckLogMissing:
		view.Status = webui.LogNotRecorded
		return view
	}
	view.Status = webui.LogAvailable
	if len(content) > maximumBrowserLogBytes {
		content = content[:maximumBrowserLogBytes]
		view.DisplayTruncated = true
	}
	view.Content = string(content)
	return view
}

// browserRuntimeView carries the same runtime observation the owner API
// reports, so both surfaces state one fact rather than two descriptions of it.
// The reason text stays out of the view: the screen has its own words for the
// bounded codes and renders an unrecognised code as data.
func browserRuntimeView(status checkapi.RuntimeStatus) webui.CheckRuntimeView {
	return webui.CheckRuntimeView{Available: status.Available, Code: status.UnavailableCode}
}

func browserCheckPolicy(policy state.CheckPolicy, exists bool) webui.CheckPolicyView {
	if !exists {
		return webui.CheckPolicyView{}
	}
	view := webui.CheckPolicyView{
		Saved:               true,
		Version:             policy.Version,
		Digest:              policy.Digest,
		ShortDigest:         shortOpaqueID(policy.Digest),
		Executor:            policy.Executor,
		AllowedEvents:       policy.AllowedEvents,
		MaxTimeoutMS:        policy.MaxTimeoutMS,
		MaxOutputLimitBytes: policy.MaxOutputLimitBytes,
		QueueLimit:          policy.QueueLimit,
		MaxActiveJobs:       policy.MaxActiveJobs,
		MaxLeaseMS:          policy.MaxLeaseMS,
		Legacy:              policy.Execution.Legacy,
		ConsentActive:       policy.ConsentActive,
		ConsentVersion:      policy.ConsentVersion,
		UpdatedAt:           policy.UpdatedAt,
	}
	view.Source = webui.CheckSourceLimitsView{
		MaxEntries:    policy.Execution.Source.MaxEntries,
		MaxFileBytes:  policy.Execution.Source.MaxFileBytes,
		MaxTotalBytes: policy.Execution.Source.MaxTotalBytes,
		MaxPathDepth:  policy.Execution.Source.MaxPathDepth,
		MaxPathBytes:  policy.Execution.Source.MaxPathBytes,
		MaxNameBytes:  policy.Execution.Source.MaxNameBytes,
		MetadataLimit: policy.Execution.Source.MetadataLimit,
	}
	view.Container = webui.CheckContainerView{
		Image:        policy.Execution.ContainerImage,
		Runtime:      policy.Execution.ContainerRuntime,
		Network:      policy.Execution.ContainerNetwork,
		CPUMillis:    policy.Execution.ContainerCPUMillis,
		MemoryBytes:  policy.Execution.ContainerMemoryBytes,
		PIDs:         policy.Execution.ContainerPIDs,
		ScratchBytes: policy.Execution.ContainerScratchBytes,
	}
	return view
}

func browserCheckJobRow(job state.CheckJob, selfURL string) webui.CheckJobRow {
	row := webui.CheckJobRow{
		ID:                   job.ID,
		ShortID:              shortOpaqueID(job.ID),
		URL:                  configuredCheckJobURL(selfURL, job.ID),
		Status:               job.Status,
		Trigger:              job.Trigger,
		TriggerRef:           job.TriggerRef,
		SourceOID:            job.SourceOID,
		SourceShortOID:       shortOID(job.SourceOID),
		BaseOID:              job.BaseOID,
		BaseShortOID:         shortOID(job.BaseOID),
		Executor:             job.Executor,
		PullRequestNumber:    job.PullRequestNumber,
		WorkflowPath:         job.WorkflowPath,
		ConfigurationVersion: job.ConfigurationVersion,
		PolicyVersion:        job.PolicyVersion,
		CancelRequested:      job.CancelRequestedAt != nil,
		Summary:              job.Summary,
		AdmittedAt:           job.AdmittedAt,
	}
	if job.PullRequestNumber > 0 {
		row.PullRequestURL = pullRequestURL(job.RepositoryID, job.PullRequestNumber)
	}
	if job.TaskID != "" {
		row.TaskURL = tasksURL(job.RepositoryID, job.TaskID)
	}
	if job.StartedAt != nil {
		row.StartedAt = *job.StartedAt
	}
	if job.FinishedAt != nil {
		row.FinishedAt = *job.FinishedAt
	}
	// A cancel is offered only while there is work it could still affect.
	//
	// The backend accepts a cancel on a finished job and records the intent
	// without reversing the result, which is the right contract for a request
	// that raced a completion. It is the wrong thing to *offer*: a control
	// beside a passed job invites the reader to believe the run can still be
	// stopped, and pressing it changes no result.
	row.Cancellable = !terminalBrowserJob(job.Status) && job.CancelRequestedAt == nil
	row.Rerunnable = terminalBrowserJob(job.Status)
	return row
}

// terminalBrowserJob reports that a job has reached a final state, whatever
// that state is. Cancelled, passed, failed and every recorded failure mode are
// all finished; only the three working states are not.
func terminalBrowserJob(status string) bool {
	switch status {
	case state.CheckJobPending, state.CheckJobClaimed, state.CheckJobStarted:
		return false
	default:
		return true
	}
}

// policyFieldRanges copies the backend's published bounds into the view's own
// shape.
//
// It is a translation, not a second source. Every number comes from
// state.CheckPolicyBoundsFor, and a field the backend publishes no range for
// is simply absent, so the screen says nothing about it rather than inventing
// a bound. This also keeps the view package free of any import of the storage
// package: the server adapter is the only thing that knows both shapes.
func policyFieldRanges() map[string]webui.FieldRange {
	ranges := make(map[string]webui.FieldRange, len(policyRangeFields))
	for _, field := range policyRangeFields {
		bounds, known := state.CheckPolicyBoundsFor(field)
		if !known {
			continue
		}
		range_ := webui.FieldRange{Min: bounds.Min, Max: bounds.Max, Known: true}
		if !bounds.HasFixedMinimum() {
			// The floor is another setting on the same screen. Name it, and
			// keep showing the maximum: a moving floor is no reason to leave
			// the ceiling enforced but undisclosed.
			range_.MinLabel = policyFloorLabels[bounds.MinField]
		}
		ranges[field] = range_
	}
	return ranges
}

// policyFloorLabels names the field a moving floor refers to, in the words the
// editor uses for it. Only fields that actually serve as a floor appear here.
var policyFloorLabels = map[string]webui.MessageCode{
	state.FieldSourceMaxFileBytes: webui.MsgCCSrcFileBytes,
}

// policyRangeFields are the numeric controls the editor draws. The names are
// the backend's field vocabulary, which is also what the form inputs are named
// and what a refusal reports.
var policyRangeFields = func() []string {
	var fields []string
	for _, limit := range webui.PolicyLimitFields() {
		fields = append(fields, limit.Field)
	}
	return fields
}()

// policyFieldDefaults is what the backend uses for each field left empty. The
// numbers come from the backend's own default tables. Fields without an entry
// have no default and must be filled in.
func policyFieldDefaults() map[string]int64 {
	source := state.DefaultCheckSourceLimits()
	container := state.DefaultCheckContainerLimits()
	return map[string]int64{
		state.FieldSourceMaxEntries:      int64(source.MaxEntries),
		state.FieldSourceMaxFileBytes:    source.MaxFileBytes,
		state.FieldSourceMaxTotalBytes:   source.MaxTotalBytes,
		state.FieldSourceMaxPathDepth:    int64(source.MaxPathDepth),
		state.FieldSourceMaxPathBytes:    int64(source.MaxPathBytes),
		state.FieldSourceMaxNameBytes:    int64(source.MaxNameBytes),
		state.FieldSourceMetadataLimit:   source.MetadataLimit,
		state.FieldContainerCPUMillis:    container.CPUMillis,
		state.FieldContainerMemoryBytes:  container.MemoryBytes,
		state.FieldContainerPIDs:         container.PIDs,
		state.FieldContainerScratchBytes: container.ScratchBytes,
	}
}

// Starting values for the limits that have no backend default, offered only
// before anything is saved. They are ordinary suggestions the owner can
// change: the time limit matches the default a check file gets when it asks
// for none, and the rest are the values the documented example policy uses.
var suggestedPolicyLimits = map[string]int64{
	state.FieldMaxTimeoutMS:        checkworkflow.DefaultTimeoutMS,
	state.FieldMaxOutputLimitBytes: 1 << 20,
	state.FieldQueueLimit:          32,
	state.FieldMaxActiveJobs:       1,
	state.FieldMaxLeaseMS:          60 * 1000,
}

// storedPolicyLimits reads every numeric field of a stored policy by its
// backend name.
func storedPolicyLimits(policy webui.CheckPolicyView) map[string]int64 {
	return map[string]int64{
		state.FieldMaxTimeoutMS:        policy.MaxTimeoutMS,
		state.FieldMaxOutputLimitBytes: policy.MaxOutputLimitBytes,
		state.FieldQueueLimit:          int64(policy.QueueLimit),
		state.FieldMaxActiveJobs:       int64(policy.MaxActiveJobs),
		state.FieldMaxLeaseMS:          policy.MaxLeaseMS,

		state.FieldSourceMaxEntries:    int64(policy.Source.MaxEntries),
		state.FieldSourceMaxFileBytes:  policy.Source.MaxFileBytes,
		state.FieldSourceMaxTotalBytes: policy.Source.MaxTotalBytes,
		state.FieldSourceMaxPathDepth:  int64(policy.Source.MaxPathDepth),
		state.FieldSourceMaxPathBytes:  int64(policy.Source.MaxPathBytes),
		state.FieldSourceMaxNameBytes:  int64(policy.Source.MaxNameBytes),
		state.FieldSourceMetadataLimit: policy.Source.MetadataLimit,

		state.FieldContainerCPUMillis:    policy.Container.CPUMillis,
		state.FieldContainerMemoryBytes:  policy.Container.MemoryBytes,
		state.FieldContainerPIDs:         policy.Container.PIDs,
		state.FieldContainerScratchBytes: policy.Container.ScratchBytes,
	}
}

// policyFormFrom fills the form from the stored policy, so a field the owner
// does not touch resubmits its saved value. Each value is written in the
// largest unit that holds it exactly, so resubmitting it unchanged stores the
// same number.
func policyFormFrom(policy webui.CheckPolicyView) webui.CheckPolicyForm {
	// Before anything is saved, the form starts from values that save as they
	// are: the mode that needs no extra runtime, both events (matching the
	// example check file), and the suggested limits. Saving still turns
	// nothing on; that stays its own step.
	values := suggestedPolicyLimits
	form := webui.CheckPolicyForm{
		Executor: webui.ExecutorHost, ContainerNetwork: webui.ContainerNetworkNone,
		PushSelected: true, PullRequestSelected: true,
	}
	if policy.Saved {
		values = storedPolicyLimits(policy)
		form = webui.CheckPolicyForm{
			Executor:            policy.Executor,
			PushSelected:        policy.AllowsPush(),
			PullRequestSelected: policy.AllowsPullRequest(),
			ContainerImage:      policy.Container.Image,
			ContainerNetwork:    policy.Container.Network,
		}
		if form.ContainerNetwork == "" {
			form.ContainerNetwork = webui.ContainerNetworkNone
		}
	}
	form.Limits = make(map[string]webui.LimitInput, len(policyRangeFields))
	for _, limit := range webui.PolicyLimitFields() {
		form.Limits[limit.Field] = webui.FormatLimit(limit.Kind, values[limit.Field], limit.Unit)
	}
	return form
}

func submittedPolicyForm(request *http.Request) webui.CheckPolicyForm {
	form := webui.CheckPolicyForm{
		Executor:            postValue(request, "executor"),
		PushSelected:        formChecked(postValue(request, "event_push")),
		PullRequestSelected: formChecked(postValue(request, "event_pull_request")),
		ContainerImage:      strings.TrimSpace(postValue(request, "container_image")),
		ContainerNetwork:    postValue(request, "container_network"),
		Limits:              make(map[string]webui.LimitInput, len(policyRangeFields)),
	}
	for _, limit := range webui.PolicyLimitFields() {
		input := webui.LimitInput{
			Amount: strings.TrimSpace(postValue(request, limit.Field)),
			Unit:   strings.TrimSpace(postValue(request, limit.UnitField())),
		}
		// A number posted without a unit is in the stored base unit, which
		// is what a script or an older page sends. Re-showing it needs a unit
		// the menu offers, so it is rewritten into one when it converts
		// cleanly; otherwise the text is kept exactly as sent.
		if input.Unit == "" && input.Amount != "" && limit.Kind != webui.LimitCount {
			if value, err := webui.ParseLimit(limit.Kind, input); err == nil && value != 0 {
				input = webui.FormatLimit(limit.Kind, value, limit.Unit)
			}
		}
		form.Limits[limit.Field] = input
	}
	return form
}

// policyInputFrom turns the submitted text into the backend's policy input.
//
// It checks only what the browser can check: a field that must be an amount
// is one and converts exactly to the stored unit, an event is selected, and
// container settings are supplied only for the mode that uses them. Every
// range, the image format, and the relationship between limits stay the
// backend's decision, so the two cannot disagree.
func policyInputFrom(repositoryID string, form webui.CheckPolicyForm) (state.CheckPolicyInput, []webui.Notice) {
	var notices []webui.Notice
	// The only thing checked here is that an amount converts to a whole
	// stored value. Whether that value is acceptable is the backend's
	// decision, reported back as a field notice, so no bound is written twice.
	// An empty field arrives as zero, which the backend answers with its own
	// rule: a default where one exists, a refusal where none does.
	value := func(field string) int64 {
		limit, known := webui.PolicyLimitFor(field)
		if !known {
			return 0
		}
		parsed, err := webui.ParseLimit(limit.Kind, form.Limit(field))
		if err != nil {
			notices = append(notices, webui.Error(field, webui.LimitNoticeCode(limit.Kind, err)))
			return 0
		}
		return parsed
	}
	// A count stored as a Go int must fit one. Every published maximum is far
	// below that, so this only turns an absurd amount into the backend's
	// ordinary range refusal instead of a silent wrap.
	count := func(field string) int {
		parsed := value(field)
		if parsed > int64(math.MaxInt32) {
			notices = append(notices, webui.Error(field, webui.MsgCCFieldRange))
			return 0
		}
		return int(parsed)
	}

	input := state.CheckPolicyInput{RepositoryID: repositoryID, Executor: form.Executor}
	if form.PushSelected {
		input.AllowedEvents = append(input.AllowedEvents, webui.CheckEventPush)
	}
	if form.PullRequestSelected {
		input.AllowedEvents = append(input.AllowedEvents, webui.CheckEventPullRequest)
	}

	input.MaxTimeoutMS = value(state.FieldMaxTimeoutMS)
	input.MaxOutputLimitBytes = value(state.FieldMaxOutputLimitBytes)
	input.QueueLimit = count(state.FieldQueueLimit)
	input.MaxActiveJobs = count(state.FieldMaxActiveJobs)
	input.MaxLeaseMS = value(state.FieldMaxLeaseMS)

	// An empty source field asks for the server's own bounded default, which
	// is what a zero means to the backend.
	input.Execution.Source = state.CheckSourceLimits{
		MaxEntries:    count(state.FieldSourceMaxEntries),
		MaxFileBytes:  value(state.FieldSourceMaxFileBytes),
		MaxTotalBytes: value(state.FieldSourceMaxTotalBytes),
		MaxPathDepth:  count(state.FieldSourceMaxPathDepth),
		MaxPathBytes:  count(state.FieldSourceMaxPathBytes),
		MaxNameBytes:  count(state.FieldSourceMaxNameBytes),
		MetadataLimit: value(state.FieldSourceMetadataLimit),
	}

	// Container settings belong to the container mode alone. Sending them with
	// another mode is what the backend refuses, so they are simply not sent.
	//
	// The image format, the network vocabulary and every range are the
	// backend's to judge. Re-checking them here would duplicate rules that can
	// drift; a refusal comes back as a field notice instead.
	if form.Executor == webui.ExecutorContainer {
		if form.ContainerNetwork == "" {
			form.ContainerNetwork = webui.ContainerNetworkNone
		}
		input.Execution.ContainerImage = form.ContainerImage
		input.Execution.ContainerNetwork = form.ContainerNetwork
		input.Execution.ContainerCPUMillis = value(state.FieldContainerCPUMillis)
		input.Execution.ContainerMemoryBytes = value(state.FieldContainerMemoryBytes)
		input.Execution.ContainerPIDs = value(state.FieldContainerPIDs)
		input.Execution.ContainerScratchBytes = value(state.FieldContainerScratchBytes)
	}
	return input, notices
}

// policySaveNotices turns a backend refusal into notices attached to the
// controls that carry the refused fields.
//
// The backend stays the only authority on what is acceptable. This function
// does not re-check anything; it reads which field the backend refused and by
// which rule, so the browser needs no second copy of any range to drift from
// the enforced one. A refusal this package cannot place still reaches the
// operator as a form-level message rather than disappearing.
func policySaveNotices(err error) []webui.Notice {
	refusals := state.PolicyFieldErrors(err)
	if len(refusals) == 0 {
		if errors.Is(err, state.ErrInvalidCheckPolicy) {
			return []webui.Notice{webui.Error("", webui.MsgCCPolicyRefused)}
		}
		return []webui.Notice{webui.Error("", webui.MsgCCFailed)}
	}
	var notices []webui.Notice
	for _, refusal := range refusals {
		field, code := webui.PolicyFieldNotice(refusal.Field, refusal.Rule)
		if field == "" {
			// An unmapped field would otherwise vanish. Report it at form
			// level instead of silently dropping the backend's answer.
			notices = append(notices, webui.Error("", webui.MsgCCPolicyRefused))
			continue
		}
		notices = append(notices, webui.Error(field, code))
	}
	return notices
}

func policySaveStatus(err error) int {
	if errors.Is(err, state.ErrInvalidCheckPolicy) {
		return http.StatusUnprocessableEntity
	}
	return http.StatusServiceUnavailable
}

// handleRunnerTokens serves the runner token screen and its forms.
func (app *App) handleRunnerTokens(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	writer.Header().Set("Cache-Control", "no-store")
	adminSession, ok := app.requireBrowserAdmin(writer, request)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionRepository, stored.ID, adminSession.CSRF)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	if request.Method == http.MethodGet {
		app.renderRunnerTokens(writer, request, stored, summary, chrome, "", "", "", nil,
			state.RunnerCredential{}, "", http.StatusOK)
		return
	}

	// Every token change, including a refusal, is private and must not be
	// cached. No response path puts a token value in a redirect URL.
	writer.Header().Set("Cache-Control", "no-store")
	if !parseForm(writer, request) {
		return
	}
	action := postValue(request, "action")
	credentialID := postValue(request, "credential_id")
	// The label is not sensitive, so a refusal can hand it back rather than
	// making the operator type it again.
	label := strings.Trim(postValue(request, "label"), " \t")
	if !constantEqual(adminSession.CSRF, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	if err := app.Auth.VerifyCredential(request.Context(), "admin", postValue(request, "admin_password"), request.RemoteAddr); err != nil {
		code, status := webui.MsgAdminFailed, http.StatusUnauthorized
		if errors.Is(err, auth.ErrRateLimited) {
			code, status = webui.MsgAdminLocked, http.StatusTooManyRequests
		}
		app.renderRunnerTokens(writer, request, stored, summary, chrome, action, credentialID, label,
			[]webui.Notice{webui.Error("admin_password", code)}, state.RunnerCredential{}, "", status)
		return
	}

	switch action {
	case webui.ActionIssueRunnerToken:
		if !validBrowserCredentialLabel(label) {
			app.renderRunnerTokens(writer, request, stored, summary, chrome, action, "", label,
				[]webui.Notice{webui.Error("label", webui.MsgRTLabelInvalid)}, state.RunnerCredential{}, "", http.StatusUnprocessableEntity)
			return
		}
		creationID := postValue(request, "creation_id")
		if !validAttemptID(creationID) {
			app.renderRunnerTokens(writer, request, stored, summary, chrome, action, "", label,
				[]webui.Notice{webui.Error("", webui.MsgRTFailed)}, state.RunnerCredential{}, "", http.StatusUnprocessableEntity)
			return
		}
		credential, token, created, err := app.Store.IssueCheckRunnerToken(request.Context(), stored.ID, label, creationID, app.now())
		if err != nil {
			code, status := webui.MsgRTFailed, http.StatusServiceUnavailable
			if errors.Is(err, state.ErrCheckPolicyMissing) {
				code, status = webui.MsgCCPolicyMissing, http.StatusConflict
			} else if errors.Is(err, state.ErrCheckRunnerCreationConflict) || errors.Is(err, state.ErrInvalidCheckJob) {
				code, status = webui.MsgRTLabelInvalid, http.StatusConflict
			}
			app.renderRunnerTokens(writer, request, stored, summary, chrome, action, "", label,
				[]webui.Notice{webui.Error("", code)}, state.RunnerCredential{}, "", status)
			return
		}
		if !created || token == "" {
			// The same request was already handled. Saying so is honest;
			// minting a second token silently would not be.
			app.renderRunnerTokens(writer, request, stored, summary, chrome, "", "", "",
				[]webui.Notice{webui.Info(webui.MsgRTExisting)}, state.RunnerCredential{}, "", http.StatusOK)
			return
		}
		app.renderRunnerTokens(writer, request, stored, summary, chrome, "", "", "",
			[]webui.Notice{webui.Success(webui.MsgRTIssued)}, credential, token, http.StatusOK)
	case webui.ActionRevokeRunnerToken:
		if !validAttemptID(credentialID) {
			app.renderRunnerTokens(writer, request, stored, summary, chrome, action, credentialID, "",
				[]webui.Notice{webui.Error("", webui.MsgRTNotFound)}, state.RunnerCredential{}, "", http.StatusConflict)
			return
		}
		if err := app.Store.RevokeCheckRunnerToken(request.Context(), stored.ID, credentialID, app.now()); err != nil {
			app.renderRunnerTokens(writer, request, stored, summary, chrome, action, credentialID, "",
				[]webui.Notice{webui.Error("", webui.MsgRTNotFound)}, state.RunnerCredential{}, "", http.StatusConflict)
			return
		}
		app.wakeChecks(stored.ID)
		app.noticeRedirect(writer, request, runnerTokensURL(stored.ID)+"?notice=runner_token_revoked", http.StatusSeeOther)
	default:
		app.renderRunnerTokens(writer, request, stored, summary, chrome, action, credentialID, "",
			[]webui.Notice{webui.Error("", webui.MsgRTFailed)}, state.RunnerCredential{}, "", http.StatusBadRequest)
	}
}

// renderRunnerTokens draws the runner token screen.
//
// pendingLabel is the label the operator typed. It is not sensitive and is put
// back into the form on a refusal so a failed attempt does not make them type
// it again. No token value is ever carried this way.
func (app *App) renderRunnerTokens(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, pendingAction, pendingCredentialID, pendingLabel string, notices []webui.Notice, issued state.RunnerCredential, issuedToken string, status int) {
	credentials, err := app.Store.CheckRunnerCredentials(request.Context(), stored.ID)
	if err != nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgRTFailed, "")
		return
	}
	_, policyExists, err := app.Store.CheckPolicy(request.Context(), stored.ID)
	if err != nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgRTFailed, "")
		return
	}
	basePage := app.baseRepositoryPage(request, chrome, stored, summary)
	self := runnerTokensURL(stored.ID)
	page := webui.RunnerCredentialsPage{
		Chrome:              chrome,
		Repo:                basePage.Repo,
		Tabs:                repositoryTabs(basePage, webui.RepoTabChecks),
		SelfURL:             self,
		SubmitURL:           self,
		ConfiguredChecksURL: configuredChecksURL(stored.ID),
		PolicyMissing:       !policyExists,
		Issued:              browserRunnerCredential(issued),
		IssuedToken:         issuedToken,
		PendingAction:       pendingAction,
		PendingCredentialID: pendingCredentialID,
		PendingLabel:        pendingLabel,
		Commands:            runnerCommands(app.baseURL(request), stored.ID),
	}
	if notices != nil {
		page.Chrome.Notices = notices
	}
	// A fresh creation identity per rendered form. A resubmitted form carries
	// the identity it was rendered with, so the backend can recognise the
	// repeat instead of issuing a second token.
	if creationID, err := state.RandomID(); err == nil {
		page.CreationID = creationID
	}
	for _, credential := range credentials {
		page.Credentials = append(page.Credentials, browserRunnerCredential(credential))
	}
	app.render(writer, status, page)
}

func browserRunnerCredential(credential state.RunnerCredential) webui.RunnerCredentialRow {
	row := webui.RunnerCredentialRow{
		ID:         credential.ID,
		ShortID:    shortOpaqueID(credential.ID),
		Label:      credential.Label,
		Generation: credential.Generation,
		CreatedAt:  credential.CreatedAt,
		Revoked:    credential.RevokedAt != nil,
	}
	if credential.RevokedAt != nil {
		row.RevokedAt = *credential.RevokedAt
	}
	if credential.LastUsedAt != nil {
		row.LastUsedAt = *credential.LastUsedAt
	}
	return row
}

// runnerCommands builds the example commands for connecting a runner.
//
// They carry a server address and a repository name and nothing else. The
// token lives in a file the runner reads, and no password appears here: a
// value on a command line reaches the process list and the shell history.
func runnerCommands(baseURL, repositoryID string) []string {
	return []string{
		"owngit runner-credential issue \\",
		"  --server " + baseURL + " \\",
		"  --repository " + repositoryID + " \\",
		"  --password-file ./admin-password \\",
		"  --label build-host \\",
		"  --token-file ./runner-token",
		"",
		"owngit runner \\",
		"  --server " + baseURL + " \\",
		"  --repository " + repositoryID + " \\",
		"  --token-file ./runner-token \\",
		"  --workspace-root /srv/owngit-runner/" + repositoryID,
	}
}
