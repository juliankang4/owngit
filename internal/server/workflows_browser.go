package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"owngit/internal/actions"
	"owngit/internal/checkrun"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
	"owngit/internal/workflows"
)

const maximumBrowserRuns = 50

func repositorySectionURL(address, rest string) string {
	return "/repositories/" + url.PathEscape(address) + "/" + rest
}

func workflowFilesURL(address string) string { return repositorySectionURL(address, "workflows") }
func workflowRunsURL(address string) string  { return repositorySectionURL(address, "workflow-runs") }
func workflowSecretsURL(address string) string {
	return repositorySectionURL(address, "workflow-secrets")
}
func workflowRunURL(address, runID string) string {
	return workflowRunsURL(address) + "/" + url.PathEscape(runID)
}
func workflowJobURL(address, runID, jobID string) string {
	return workflowRunURL(address, runID) + "/jobs/" + url.PathEscape(jobID)
}
func workflowDispatchURL(address, path, ref string) string {
	query := url.Values{"path": {path}}
	if ref != "" {
		query.Set("ref", ref)
	}
	return workflowFilesURL(address) + "/run?" + query.Encode()
}

func checksNav(address, active string) webui.ChecksNav {
	return webui.ChecksNav{TasksURL: tasksURL(address, ""), WorkflowsURL: workflowFilesURL(address), RunsURL: workflowRunsURL(address), Active: active}
}

var workflowNotices = map[string][]webui.Notice{
	"workflow_turned_on":      {webui.Success("wf.notice.turned_on")},
	"workflow_dispatched":     {webui.Success("wf.notice.dispatched")},
	"workflow_cancel":         {webui.Success("wf.notice.cancel")},
	"workflow_rerun":          {webui.Success("wf.notice.rerun")},
	"workflow_rerun_existing": {webui.Info("wf.notice.rerun_existing")},
	"workflow_secret_saved":   {webui.Success("wf.notice.secret_saved")},
	"workflow_secret_removed": {webui.Success("wf.notice.secret_removed")},
}

func (app *App) handleWorkflowPages(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, parts []string) {
	get, post := request.Method == http.MethodGet, request.Method == http.MethodPost
	switch {
	case parts[0] == "workflows" && len(parts) == 1 && get:
		app.renderWorkflowFiles(writer, request, stored, summary, chrome, request.URL.Query().Get("ref"), http.StatusOK)
	case parts[0] == "workflows" && len(parts) == 1 && post:
		app.turnOnWorkflows(writer, request, stored, summary, chrome)
	case parts[0] == "workflows" && len(parts) == 2 && parts[1] == "run" && get:
		app.renderWorkflowDispatch(writer, request, stored, summary, chrome, request.URL.Query().Get("path"), request.URL.Query().Get("ref"), nil, nil, http.StatusOK)
	case parts[0] == "workflows" && len(parts) == 2 && parts[1] == "run" && post:
		app.dispatchWorkflow(writer, request, stored, summary, chrome)
	case parts[0] == "workflow-runs" && len(parts) == 1 && get:
		app.renderWorkflowRuns(writer, request, stored, summary, chrome)
	case parts[0] == "workflow-runs" && len(parts) == 2 && get:
		app.renderWorkflowRun(writer, request, stored, summary, chrome, parts[1], nil, http.StatusOK)
	case parts[0] == "workflow-runs" && len(parts) == 3 && (parts[2] == "cancel" || parts[2] == "rerun") && post:
		app.changeWorkflowRun(writer, request, stored, summary, chrome, parts[1], parts[2])
	case parts[0] == "workflow-runs" && len(parts) == 4 && parts[2] == "jobs" && get:
		app.renderWorkflowJob(writer, request, stored, summary, chrome, parts[1], parts[3])
	case parts[0] == "workflow-runs" && len(parts) == 5 && parts[2] == "jobs" && parts[4] == "log" && get:
		app.serveWorkflowJobLog(writer, request, stored, parts[1], parts[3])
	default:
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, request.URL.Path)
	}
}

func (app *App) workflowsPage(request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, view, section string) webui.WorkflowsPage {
	base := app.baseRepositoryPage(request, chrome, stored, summary)
	return webui.WorkflowsPage{Chrome: chrome, Repo: base.Repo, Tabs: repositoryTabs(base, webui.RepoTabChecks), Nav: checksNav(stored.Address, section), View: view, SelfURL: chrome.CurrentURL}
}

func workflowMessage(message actions.Message) webui.WorkflowMessage {
	return webui.WorkflowMessage{Code: message.Code, Path: message.Path, Line: message.Line, Detail: message.Detail, Args: message.Args}
}

func workflowMessages(messages []actions.Message) []webui.WorkflowMessage {
	result := make([]webui.WorkflowMessage, 0, len(messages))
	for _, message := range messages {
		result = append(result, workflowMessage(message))
	}
	return result
}

func workflowProblem(request *http.Request, step string, err error) (webui.WorkflowMessage, int) {
	var refusal *actions.Refusal
	var problem *workflows.Problem
	switch {
	case errors.As(err, &problem):
		message := webui.WorkflowMessage{Code: problem.Code, Detail: problem.Message}
		if details, ok := problem.Details.(map[string]string); ok && problem.Code == "workflow.moved" {
			message.Args = map[string]string{"branch": details["ref"], "oid": shortOID(details["source_oid"])}
		}
		return message, problem.Status
	case errors.As(err, &refusal):
		status := http.StatusConflict
		if refusal.Code == "workflow.dispatch_input" {
			status = http.StatusUnprocessableEntity
		}
		return workflowMessage(refusal.Message), status
	case errors.Is(err, state.ErrActionsWorkflowsOff), errors.Is(err, state.ErrCheckConsentRequired), errors.Is(err, state.ErrCheckPolicyMissing):
		return webui.WorkflowMessage{Code: "workflow.off", Detail: "Workflows are off or need current administrator consent."}, http.StatusConflict
	case errors.Is(err, state.ErrCheckEventNotAllowed):
		return webui.WorkflowMessage{Code: "workflow.event_off", Detail: "The check policy does not allow this event."}, http.StatusConflict
	case errors.Is(err, repository.ErrPinnedRepositoryBusy):
		return webui.WorkflowMessage{Code: "wf.error.busy"}, http.StatusServiceUnavailable
	default:
		return webui.WorkflowMessage{Code: "wf.unavailable"}, unavailable(request, step, err)
	}
}

func (app *App) renderWorkflowFiles(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, ref string, status int) {
	page := app.workflowsPage(request, stored, summary, chrome, webui.WorkflowViewFiles, webui.ChecksSectionWorkflows)
	page.SelfURL, page.SubmitURL = workflowFilesURL(stored.Address), workflowFilesURL(stored.Address)
	view := &webui.WorkflowFilesView{SecretsURL: workflowSecretsURL(stored.Address), AutomaticChecksURL: configuredChecksURL(stored.Address)}
	page.Files = view
	if summary.Empty || summary.DefaultOID == "" {
		view.NoCommits = true
		app.render(writer, request, status, page)
		return
	}
	if ref != "" {
		page.SelfURL += "?ref=" + url.QueryEscape(ref)
	}
	service := workflows.Service{Store: app.Store, Repositories: app.Repositories}
	discovery, err := service.Discover(request.Context(), stored.ID, ref)
	if err != nil {
		message, problemStatus := workflowProblem(request, "workflow discovery", err)
		page.Problem, page.Files = &message, nil
		app.render(writer, request, problemStatus, page)
		return
	}
	policy, exists, err := app.Store.CheckPolicy(request.Context(), stored.ID)
	if err != nil {
		page.Unavailable = true
		app.render(writer, request, unavailable(request, "check policy read", err), page)
		return
	}
	view.Branch, view.SourceOID, view.ShortOID = discovery.Ref, discovery.SourceOID, shortOID(discovery.SourceOID)
	view.Branches = commitBranches(summary)
	allowed := map[string]bool{}
	for _, event := range discovery.AllowedEvents {
		allowed[event] = true
	}
	view.Status = webui.WorkflowSwitch{PolicySaved: exists, RunWorkflows: discovery.RunWorkflows, ConsentActive: discovery.ConsentActive, Version: policy.Version}
	for _, event := range []string{"push", "pull_request", state.ActionsEventDispatch, state.ActionsEventSchedule} {
		view.Status.Events = append(view.Status.Events, webui.WorkflowEventState{Event: event, Allowed: allowed[event]})
	}
	for _, file := range discovery.Workflows {
		view.Files = append(view.Files, browserWorkflowFile(stored.Address, discovery.Ref, file, allowed))
	}
	if input, reviewable := workflowTurnOnInput(stored.ID, policy, exists); !view.Status.On() && reviewable {
		review, err := app.workflowTurnOnReview(request, input, policy)
		if err != nil {
			logFailure(request, "workflow turn-on review", err)
			page.Chrome.Notices = append(page.Chrome.Notices, webui.Error("", webui.MsgCCPolicyRefused))
		} else {
			for _, file := range view.Files {
				switch file.Verdict {
				case webui.VerdictRuns:
					review.Run++
				case webui.VerdictNoted:
					review.Noted++
				case webui.VerdictPartial:
					review.Partial++
				case webui.VerdictRefused:
					review.Refused++
				case webui.VerdictNeverFits:
					review.NeverFits++
				default:
					review.Unknown++
				}
			}
			view.Status.Review = review
		}
	}
	app.render(writer, request, status, page)
}

func commitBranches(summary repository.Summary) []string {
	var names []string
	for _, branch := range summary.Branches {
		if branch.Type == "commit" {
			names = append(names, branch.Name)
		}
	}
	return names
}

func browserWorkflowFile(address, ref string, file workflows.File, allowed map[string]bool) webui.WorkflowFileView {
	view := webui.WorkflowFileView{Path: file.Path, Name: file.Name, ShortOID: shortOID(file.OID), Notes: workflowMessages(file.Notes), ExpandedJobs: file.ExpandedJobs, ExpansionKnown: file.ExpansionKnown}
	if file.Refusal != nil {
		message := workflowMessage(*file.Refusal)
		view.Refusal, view.Verdict = &message, webui.VerdictRefused
		return view
	}
	events := make([]string, 0, len(file.Triggers))
	for event := range file.Triggers {
		events = append(events, event)
	}
	sort.Strings(events)
	for _, event := range events {
		trigger := file.Triggers[event]
		item := webui.WorkflowTriggerView{Event: event, Allowed: allowed[event], Notes: workflowMessages(trigger.Notes)}
		for _, filter := range []struct {
			label  webui.MessageCode
			values []string
		}{
			{"wf.filter.branches", trigger.Branches}, {"wf.filter.branches_ignore", trigger.BranchesIgnore},
			{"wf.filter.tags", trigger.Tags}, {"wf.filter.tags_ignore", trigger.TagsIgnore},
			{"wf.filter.paths", trigger.Paths}, {"wf.filter.paths_ignore", trigger.PathsIgnore},
			{"wf.filter.types", trigger.Types}, {"wf.filter.cron", trigger.Schedules},
		} {
			if len(filter.values) > 0 {
				item.Filters = append(item.Filters, webui.WorkflowFilter{Label: filter.label, Values: filter.values})
			}
		}
		if !item.Allowed {
			item.Notes = append(item.Notes, webui.WorkflowMessage{Code: "workflow.event_off", Args: map[string]string{"event": event}, Detail: "The check policy does not allow " + event + "."})
		}
		view.Triggers = append(view.Triggers, item)
		if event == state.ActionsEventDispatch {
			view.DispatchURL = workflowDispatchURL(address, file.Path, ref)
			view.Inputs = browserWorkflowInputs(trigger.Inputs)
		}
	}
	for _, schedule := range file.Schedules {
		item := webui.WorkflowScheduleView{Cron: schedule.Cron, NextDueAt: schedule.NextDueAt.UTC(), Paused: schedule.Paused, Notes: workflowMessages(schedule.Notes)}
		if schedule.LastAdmittedAt != nil {
			item.LastAdmittedAt = schedule.LastAdmittedAt.UTC()
		}
		if schedule.LastRunID != "" {
			item.LastRunURL = workflowRunURL(address, schedule.LastRunID)
		}
		if schedule.PauseNote != "" {
			item.Notes = append(item.Notes, webui.WorkflowMessage{Code: "note.schedule_paused", Detail: schedule.PauseNote})
		}
		view.Schedules = append(view.Schedules, item)
	}
	refused := 0
	for _, job := range file.Jobs {
		item := webui.WorkflowJobPreview{Key: job.Key, Name: job.Name, RunsOn: job.RunsOn, Needs: job.Needs, Verdict: webui.VerdictRuns}
		for _, step := range job.Steps {
			item.Steps = append(item.Steps, webui.WorkflowStepLine{Name: step.Name, Command: step.Command, Uses: step.Uses})
			if step.Uses != "" {
				item.Verdict = webui.VerdictNoted
			}
		}
		if job.Refusal != nil {
			message := workflowMessage(*job.Refusal)
			item.Refusal, item.Verdict = &message, webui.VerdictRefused
			refused++
		}
		view.Jobs = append(view.Jobs, item)
	}
	switch {
	case file.NeverFits:
		view.Verdict = webui.VerdictNeverFits
	case len(file.Jobs) > 0 && refused == len(file.Jobs):
		view.Verdict = webui.VerdictRefused
	case refused > 0:
		view.Verdict = webui.VerdictPartial
	case !file.ExpansionKnown:
		view.Verdict = webui.VerdictUnknown
	case len(view.Notes) > 0:
		view.Verdict = webui.VerdictNoted
	default:
		view.Verdict = webui.VerdictRuns
		for _, job := range view.Jobs {
			if job.Verdict == webui.VerdictNoted {
				view.Verdict = webui.VerdictNoted
			}
		}
	}
	return view
}

func browserWorkflowInputs(inputs map[string]workflows.Input) []webui.WorkflowInputView {
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	views := make([]webui.WorkflowInputView, 0, len(names))
	for _, name := range names {
		input := inputs[name]
		view := webui.WorkflowInputView{Name: name, Description: input.Description, Type: input.Type, Required: input.Required, Options: input.Options}
		if input.Default != nil {
			view.Default, _ = actions.ScalarString(input.Default)
		}
		views = append(views, view)
	}
	return views
}

func workflowTurnOnInput(repositoryID string, policy state.CheckPolicy, exists bool) (input state.CheckPolicyInput, reviewable bool) {
	if !exists {
		input, notices := policyInputFrom(repositoryID, policyFormFrom(webui.CheckPolicyView{}))
		return input, len(notices) == 0
	}
	if policy.Execution.Legacy {
		return state.CheckPolicyInput{}, false
	}
	on := true
	return state.CheckPolicyInput{
		RunWorkflows: &on, RepositoryID: repositoryID, Executor: policy.Executor, AllowedEvents: policy.AllowedEvents,
		MaxTimeoutMS: policy.MaxTimeoutMS, MaxOutputLimitBytes: policy.MaxOutputLimitBytes, QueueLimit: policy.QueueLimit,
		MaxActiveJobs: policy.MaxActiveJobs, MaxLeaseMS: policy.MaxLeaseMS, Execution: policy.Execution,
	}, true
}

func (app *App) workflowTurnOnReview(request *http.Request, input state.CheckPolicyInput, policy state.CheckPolicy) (*webui.WorkflowTurnOn, error) {
	candidate, err := app.Store.CandidateCheckPolicy(request.Context(), input)
	if err != nil {
		return nil, err
	}
	return &webui.WorkflowTurnOn{Executor: candidate.Executor, Events: candidate.AllowedEvents, QueueLimit: candidate.QueueLimit,
		MaxTimeoutMS: candidate.MaxTimeoutMS, Digest: candidate.Digest, BaseVersion: policy.Version, BaseDigest: policy.Digest}, nil
}

func (app *App) turnOnWorkflows(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	if !app.parseForm(writer, request) || !app.requireCSRF(writer, request) {
		return
	}
	ref := postValue(request, "ref")
	if postValue(request, "action") != webui.ActionTurnOnWorkflows {
		app.renderError(writer, request, http.StatusBadRequest, webui.MsgSettingsUnknownAct, "")
		return
	}
	refuse := func(notice webui.Notice, status int) {
		chrome.Notices = append(chrome.Notices, notice)
		app.renderWorkflowFiles(writer, request, stored, summary, chrome, ref, status)
	}
	if _, err := app.confirmAdmin(writer, request, &chrome, false); err != nil {
		notice, status := adminPasswordNotice(request, err, "admin_password")
		refuse(notice, status)
		return
	}
	policy, exists, err := app.Store.CheckPolicy(request.Context(), stored.ID)
	if err != nil {
		refuse(webui.Error("", "wf.error.failed"), unavailable(request, "check policy read", err))
		return
	}
	input, reviewable := workflowTurnOnInput(stored.ID, policy, exists)
	base := state.ExpectedCheckPolicy{Digest: postValue(request, "base_digest")}
	version, parseErr := strconv.ParseInt(postValue(request, "base_version"), 10, 64)
	base.Version = version
	candidate, err := app.Store.CandidateCheckPolicy(request.Context(), input)
	if !reviewable || parseErr != nil || err != nil || candidate.Digest != postValue(request, "review_digest") {
		refuse(webui.Error("", "wf.error.policy_changed"), http.StatusConflict)
		return
	}
	if _, err := app.Store.SaveCheckPolicyAndGrantConsent(request.Context(), input, &base, app.now()); err != nil {
		switch {
		case errors.Is(err, state.ErrCheckPolicyStale):
			refuse(webui.Error("", "wf.error.policy_changed"), http.StatusConflict)
		case errors.Is(err, state.ErrInvalidCheckPolicy), errors.As(err, new(*state.PolicyError)):
			refuse(webui.Error("", webui.MsgCCPolicyRefused), http.StatusConflict)
		default:
			refuse(webui.Error("", "wf.error.failed"), unavailable(request, "workflow turn on", err))
		}
		return
	}
	app.wakeChecks(stored.ID)
	target := workflowFilesURL(stored.Address) + "?"
	if ref != "" {
		target += "ref=" + url.QueryEscape(ref) + "&"
	}
	app.noticeRedirect(writer, request, target+"notice=workflow_turned_on")
}

func (app *App) renderWorkflowRuns(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	page := app.workflowsPage(request, stored, summary, chrome, webui.WorkflowViewRuns, webui.ChecksSectionRuns)
	runs, err := app.Store.ActionsRuns(request.Context(), stored.ID, maximumBrowserRuns+1)
	if err != nil {
		page.Unavailable = true
		app.render(writer, request, unavailable(request, "workflow run list read", err), page)
		return
	}
	view := &webui.WorkflowRunsView{Limit: maximumBrowserRuns, Truncated: len(runs) > maximumBrowserRuns}
	if view.Truncated {
		runs = runs[:maximumBrowserRuns]
	}
	for _, run := range runs {
		item, err := app.Store.ActionsRunSummary(request.Context(), run)
		if err != nil {
			page.Unavailable = true
			app.render(writer, request, unavailable(request, "workflow run summary read", err), page)
			return
		}
		view.Runs = append(view.Runs, browserRunRow(stored.Address, item, run.SourceOID))
	}
	page.Runs = view
	app.render(writer, request, http.StatusOK, page)
}

func browserRunRow(address string, item state.ActionsRunSummary, sourceOID string) webui.WorkflowRunRow {
	c := item.Counts
	return webui.WorkflowRunRow{URL: workflowRunURL(address, item.ID), WorkflowPath: item.WorkflowPath, Event: item.Event, Conclusion: item.Conclusion,
		CreatedAt: item.CreatedAt.Local(), Stale: item.Stale, ShortOID: shortOID(sourceOID),
		Counts: webui.WorkflowCounts{Total: c.Total, Waiting: c.Waiting, Queued: c.Queued, Running: c.Running, Passed: c.Passed, Failed: c.Failed,
			Cancelled: c.Cancelled, Skipped: c.Skipped, Incomplete: c.Incomplete, Refused: c.Refused}}
}

func (app *App) readWorkflowRun(request *http.Request, repositoryID, runID string) (state.ActionsRun, bool, error) {
	if !validAttemptID(runID) {
		return state.ActionsRun{}, false, nil
	}
	return app.Store.ActionsRun(request.Context(), repositoryID, runID)
}

func (app *App) renderWorkflowRun(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, runID string, problem *webui.WorkflowMessage, status int) {
	page := app.workflowsPage(request, stored, summary, chrome, webui.WorkflowViewRun, webui.ChecksSectionRuns)
	page.SelfURL, page.SubmitURL, page.Problem = workflowRunURL(stored.Address, runID), workflowRunURL(stored.Address, runID), problem
	run, found, err := app.readWorkflowRun(request, stored.ID, runID)
	if err == nil && !found {
		page.NotFound = true
		app.render(writer, request, http.StatusNotFound, page)
		return
	}
	var view *webui.WorkflowRunView
	if err == nil {
		view, err = app.browserWorkflowRun(request, stored.Address, run)
	}
	if err != nil {
		page.Unavailable = true
		app.render(writer, request, unavailable(request, "workflow run read", err), page)
		return
	}
	page.Run = view
	app.render(writer, request, status, page)
}

func (app *App) browserWorkflowRun(request *http.Request, address string, run state.ActionsRun) (*webui.WorkflowRunView, error) {
	evidence, err := app.Store.ActionsRunEvidence(request.Context(), run)
	if err != nil {
		return nil, err
	}
	jobs, err := app.Store.ActionsRunJobs(request.Context(), run.RepositoryID, run.ID)
	if err != nil {
		return nil, err
	}
	view := &webui.WorkflowRunView{ID: run.ID, Number: run.Number, WorkflowPath: run.WorkflowPath, WorkflowName: run.Facts.WorkflowName, Event: run.Event,
		Branch: run.TriggerRef, SourceOID: run.SourceOID, ShortOID: shortOID(run.SourceOID), Conclusion: evidence.Conclusion, CreatedAt: run.CreatedAt.Local(),
		RerunGeneration: run.RerunGeneration, PullRequest: run.PullRequestNumber, Notes: workflowMessages(run.Facts.Notes), CancelRequested: run.CancelRequestedAt != nil}
	if run.ScheduledFor != nil {
		view.ScheduledFor = run.ScheduledFor.UTC()
	}
	if run.PullRequestNumber > 0 {
		view.PullRequestURL = pullRequestURL(address, run.PullRequestNumber)
	}
	finished := evidence.Conclusion != actions.StatusQueued && evidence.Conclusion != actions.StatusRunning
	view.Cancellable, view.Rerunnable = !finished && run.CancelRequestedAt == nil, finished
	var inputs map[string]any
	if err := json.Unmarshal([]byte(run.InputsJSON), &inputs); err == nil {
		for name, value := range inputs {
			text, err := actions.ScalarString(value)
			if err != nil {
				encoded, _ := json.Marshal(value)
				text = string(encoded)
			}
			view.Inputs = append(view.Inputs, webui.WorkflowValue{Name: name, Value: text})
		}
		sort.Slice(view.Inputs, func(i, j int) bool { return view.Inputs[i].Name < view.Inputs[j].Name })
	}
	for _, refused := range run.Facts.RefusedJobs {
		view.Refused = append(view.Refused, webui.WorkflowRefusedJob{Key: refused.JobKey, Reason: workflowMessage(refused.Reason)})
	}
	for _, job := range jobs {
		record, err := app.workflowJobRecord(request, job)
		if err != nil {
			return nil, err
		}
		view.Jobs = append(view.Jobs, browserWorkflowJob(address, run.ID, workflowJobSummary(record), job))
	}
	names, err := app.Store.ListWorkflowSecrets(request.Context(), run.RepositoryID)
	view.SecretsKnown = err == nil
	set := map[string]bool{}
	for _, name := range names {
		set[name.Name] = true
	}
	for _, name := range run.Facts.SecretNames {
		view.Secrets = append(view.Secrets, webui.WorkflowSecretUse{Name: name, Set: set[strings.ToUpper(name)]})
	}
	return view, nil
}

func browserWorkflowJob(address, runID string, record workflowJobView, job state.CheckJob) webui.WorkflowJobRow {
	row := webui.WorkflowJobRow{ID: job.ID, Key: job.JobKey, MatrixIndex: job.MatrixIndex, URL: workflowJobURL(address, runID, job.ID), Status: job.Status,
		Tolerated: job.Tolerated, CancelRequested: job.CancelRequestedAt != nil, Summary: job.Summary, Outcome: record.Outcome}
	if job.StartedAt != nil {
		row.StartedAt = job.StartedAt.Local()
	}
	if job.FinishedAt != nil {
		row.FinishedAt = job.FinishedAt.Local()
	}
	if record.Attempt != nil && len(record.Attempt.Results) > 0 {
		for _, result := range record.Attempt.Results {
			step := webui.WorkflowStepRow{Name: result.Name, Command: result.Command, Status: result.Status, Role: result.Role, DurationMS: result.DurationMS,
				Excerpt: result.OutputExcerpt, Truncated: result.Truncated, OutputLimitExceededBytes: result.OutputLimitExceededBytes, CleanupError: result.CleanupError, CleanupFailed: result.CleanupError != ""}
			if result.ExitCode != nil {
				step.ExitCode = strconv.Itoa(*result.ExitCode)
			}
			row.Steps = append(row.Steps, step)
		}
		return row
	}
	for _, step := range record.Steps {
		item := webui.WorkflowStepRow{Name: step.Name, Status: step.Status, Role: step.Role, DurationMS: step.DurationMS, CleanupFailed: step.CleanupFailed, Truncated: step.Truncated}
		if step.ExitCode != nil {
			item.ExitCode = strconv.Itoa(*step.ExitCode)
		}
		row.Steps = append(row.Steps, item)
	}
	if len(row.Steps) == 0 && record.Job != nil {
		for _, check := range record.Job.Checks {
			row.Steps = append(row.Steps, webui.WorkflowStepRow{Name: check.Name, Command: check.Command})
		}
	}
	return row
}

func (app *App) changeWorkflowRun(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, runID, action string) {
	if !app.parseForm(writer, request) || !app.requireCSRF(writer, request) {
		return
	}
	run, found, err := app.readWorkflowRun(request, stored.ID, runID)
	if err == nil && !found {
		app.renderWorkflowRun(writer, request, stored, summary, chrome, runID, nil, http.StatusNotFound)
		return
	}
	notice, deduped := "workflow_cancel", false
	if err == nil {
		if action == webui.ActionCancelRun {
			run, err = app.Store.CancelActionsRun(request.Context(), stored.ID, run.ID, app.now())
		} else {
			coordinator := checkrun.Coordinator{Store: app.Store, Repositories: app.Repositories}
			run, deduped, err = coordinator.RerunActionsRun(request.Context(), stored.ID, run.ID)
			notice = "workflow_rerun"
			if deduped {
				notice = "workflow_rerun_existing"
			}
		}
	}
	if err != nil {
		message, status := workflowProblem(request, "workflow run "+action, err)
		app.renderWorkflowRun(writer, request, stored, summary, chrome, runID, &message, status)
		return
	}
	app.wakeChecks(stored.ID)
	app.noticeRedirect(writer, request, workflowRunURL(stored.Address, run.ID)+"?notice="+notice)
}

func (app *App) readWorkflowJob(request *http.Request, repositoryID, runID, jobID string) (state.ActionsRun, state.CheckJob, bool, error) {
	run, found, err := app.readWorkflowRun(request, repositoryID, runID)
	if err != nil || !found || !validAttemptID(jobID) {
		return run, state.CheckJob{}, false, err
	}
	job, found, err := app.Store.CheckJob(request.Context(), repositoryID, jobID)
	return run, job, found && job.RunID == run.ID, err
}

func (app *App) renderWorkflowJob(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, runID, jobID string) {
	page := app.workflowsPage(request, stored, summary, chrome, webui.WorkflowViewJob, webui.ChecksSectionRuns)
	run, job, found, err := app.readWorkflowJob(request, stored.ID, runID, jobID)
	switch {
	case err != nil:
		page.Unavailable = true
		app.render(writer, request, unavailable(request, "workflow job read", err), page)
		return
	case !found:
		page.NotFound = true
		app.render(writer, request, http.StatusNotFound, page)
		return
	}
	view := &webui.WorkflowJobView{RunURL: workflowRunURL(stored.Address, run.ID), RunNumber: run.Number, WorkflowPath: run.WorkflowPath}
	record, err := app.workflowJobRecord(request, job)
	if err != nil {
		logFailure(request, "workflow job record read", err)
		view.Unreadable = true
		view.Job = browserWorkflowJob(stored.Address, run.ID, workflowJobView{}, job)
	} else {
		view.Job = browserWorkflowJob(stored.Address, run.ID, record, job)
	}
	if job.AttemptID != "" && !view.Unreadable {
		attempt, err := app.jobAttempt(request.Context(), job)
		if err == nil {
			view.LogStatus, view.LogTruncated = webui.LogUnavailable, attempt.LogTruncated
			if retention, err := app.Store.CheckLogRetention(request.Context()); err == nil {
				view.LogStatus = webui.LogStatusOf(app.Store.CheckLogState(attempt, retention, app.now()))
				if expires := retention.LogExpiry(attempt); expires != nil {
					view.LogExpiresAt = expires.Local()
				}
			}
			if view.LogStatus == webui.LogAvailable {
				view.LogURL = workflowJobURL(stored.Address, run.ID, job.ID) + "/log"
			}
		} else {
			view.Unreadable = true
		}
	}
	page.Job = view
	app.render(writer, request, http.StatusOK, page)
}

func (app *App) serveWorkflowJobLog(writer http.ResponseWriter, request *http.Request, stored state.Repository, runID, jobID string) {
	_, job, found, err := app.readWorkflowJob(request, stored.ID, runID, jobID)
	switch {
	case err != nil:
		app.renderError(writer, request, unavailable(request, "workflow job read", err), webui.MsgErrUnavailable, "")
		return
	case !found || job.AttemptID == "":
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, "")
		return
	}
	attempt, err := app.jobAttempt(request.Context(), job)
	var content []byte
	logState := ""
	if err == nil {
		var retention state.CheckLogRetention
		if retention, err = app.Store.CheckLogRetention(request.Context()); err == nil {
			content, logState, err = app.Store.ReadCheckLog(attempt, retention, app.now())
		}
	}
	switch {
	case err != nil:
		app.renderError(writer, request, unavailable(request, "workflow job log read", err), webui.MsgErrUnavailable, "")
	case logState == state.CheckLogExpired || logState == state.CheckLogMissing:
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, "")
	default:
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(content)
	}
}

func (app *App) renderWorkflowDispatch(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, path, ref string, values map[string]string, problem *webui.WorkflowMessage, status int) {
	page := app.workflowsPage(request, stored, summary, chrome, webui.WorkflowViewDispatch, webui.ChecksSectionWorkflows)
	page.SelfURL, page.SubmitURL, page.Problem = workflowDispatchURL(stored.Address, path, ref), workflowFilesURL(stored.Address)+"/run", problem
	discovery, err := workflows.Service{Store: app.Store, Repositories: app.Repositories}.Discover(request.Context(), stored.ID, ref)
	if err != nil {
		message, problemStatus := workflowProblem(request, "workflow discovery", err)
		page.Problem = &message
		app.render(writer, request, problemStatus, page)
		return
	}
	var file *workflows.File
	for i := range discovery.Workflows {
		if discovery.Workflows[i].Path == path {
			file = &discovery.Workflows[i]
		}
	}
	trigger, dispatchable := workflows.Trigger{}, false
	if file != nil && file.Triggers != nil {
		trigger, dispatchable = file.Triggers[state.ActionsEventDispatch]
	}
	if !dispatchable {
		page.NotFound = true
		app.render(writer, request, http.StatusNotFound, page)
		return
	}
	view := &webui.WorkflowDispatchView{Path: file.Path, Name: file.Name, Branch: discovery.Ref, Branches: commitBranches(summary), SourceOID: discovery.SourceOID,
		ShortOID: shortOID(discovery.SourceOID), BackURL: workflowFilesURL(stored.Address) + "?ref=" + url.QueryEscape(discovery.Ref)}
	allowed := false
	for _, event := range discovery.AllowedEvents {
		allowed = allowed || event == state.ActionsEventDispatch
	}
	switch {
	case file.Refusal != nil:
		message := workflowMessage(*file.Refusal)
		view.Blocked = &message
	case !discovery.RunWorkflows || !discovery.ConsentActive:
		view.Blocked = &webui.WorkflowMessage{Code: "workflow.off"}
	case !allowed:
		view.Blocked = &webui.WorkflowMessage{Code: "workflow.event_off", Args: map[string]string{"event": state.ActionsEventDispatch}, Detail: "The check policy does not allow workflow_dispatch."}
	}
	for _, input := range browserWorkflowInputs(trigger.Inputs) {
		field := webui.WorkflowInputField{WorkflowInputView: input, Value: input.Default}
		if value, ok := values[input.Name]; ok {
			field.Value = value
		}
		field.Checked = field.Value == "true"
		view.Inputs = append(view.Inputs, field)
	}
	page.Dispatch = view
	app.render(writer, request, status, page)
}

func (app *App) dispatchWorkflow(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	if !app.parseForm(writer, request) || !app.requireCSRF(writer, request) {
		return
	}
	path, ref, expected := postValue(request, "path"), postValue(request, "ref"), postValue(request, "expected_oid")
	input := workflows.DispatchInput{Path: path, Ref: ref, ExpectedOID: expected, Inputs: map[string]any{}}
	values := map[string]string{}
	for key, submitted := range request.PostForm {
		name, isInput := strings.CutPrefix(key, "input.")
		if !isInput || len(submitted) == 0 {
			continue
		}
		// A checkbox posts its hidden false first and true when checked.
		value := submitted[len(submitted)-1]
		values[name] = value
		if value != "" {
			input.Inputs[name] = value
		}
	}
	if !validOID(expected) {
		message := webui.WorkflowMessage{Code: "workflow.invalid", Detail: "expected_oid must be a commit object ID."}
		app.renderWorkflowDispatch(writer, request, stored, summary, chrome, path, ref, values, &message, http.StatusUnprocessableEntity)
		return
	}
	run, err := workflows.Service{Store: app.Store, Repositories: app.Repositories}.Dispatch(request.Context(), stored.ID, input, generalAccessActor)
	if err != nil {
		message, status := workflowProblem(request, "workflow dispatch", err)
		app.renderWorkflowDispatch(writer, request, stored, summary, chrome, path, ref, values, &message, status)
		return
	}
	app.wakeChecks(stored.ID)
	app.noticeRedirect(writer, request, workflowRunURL(stored.Address, run.ID)+"?notice=workflow_dispatched")
}

func (app *App) browserEvidenceLanes(ctx context.Context, repositoryID, address string, evidence state.RevisionCheckEvidence) *webui.EvidenceLanes {
	return evidenceLanes(address, evidence, app.staleRunOID(ctx, repositoryID))
}

func (app *App) staleRunOID(ctx context.Context, repositoryID string) func(string) string {
	return func(runID string) string {
		if run, found, err := app.Store.ActionsRun(ctx, repositoryID, runID); err == nil && found {
			return run.SourceOID
		}
		return ""
	}
}

func evidenceLanes(address string, evidence state.RevisionCheckEvidence, staleOID func(runID string) string) *webui.EvidenceLanes {
	lanes := &webui.EvidenceLanes{Conclusion: evidence.Conclusion, RevisionShortOID: shortOID(evidence.RevisionOID), WorkflowsTotal: evidence.WorkflowsTotal,
		WorkflowsTruncated: evidence.WorkflowsTruncated, Stale: evidence.HasStaleEvidence()}
	if evidence.JSONConclusion != "" {
		lanes.JSON = &webui.EvidenceLane{Conclusion: evidence.JSONConclusion, ShortOID: shortOID(evidence.JSONRevisionOID), Stale: evidence.JSONStale}
		if evidence.JSONAttempt != nil && evidence.JSONAttempt.TaskID != "" {
			lanes.JSON.URL = tasksURL(address, evidence.JSONAttempt.TaskID)
		}
	}
	for _, item := range evidence.Workflows {
		oid := evidence.RevisionOID
		if item.Stale {
			oid = staleOID(item.ID)
		}
		lanes.Workflows = append(lanes.Workflows, browserRunRow(address, item, oid))
	}
	return lanes
}
