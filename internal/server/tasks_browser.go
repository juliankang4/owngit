package server

import (
	"context"
	"net/http"

	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func (app *App) handleTasksGet(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	basePage := app.baseRepositoryPage(request, chrome, stored, summary)
	page := webui.TasksPage{
		Chrome:  chrome,
		Repo:    basePage.Repo,
		Tabs:    repositoryTabs(basePage, webui.RepoTabChecks),
		ListURL: basePage.TasksURL,
	}
	// Both links are offered to every viewer. The routes they open are
	// administrator only and send anyone else to the login, which returns
	// to the linked page afterwards.
	page.HelperURL = basePage.Repo.URL + "/helper-credentials"
	page.ConfiguredChecksURL = configuredChecksURL(stored.Address)

	configuration, configured, err := app.Store.LatestCheckConfiguration(request.Context(), stored.ID)
	if err != nil {
		app.renderError(writer, request, unavailable(request, "check configuration read", err), webui.MsgTasksUnavail, "")
		return
	}
	page.Configuration = browserCheckConfiguration(configuration, configured)

	answerUnavailable := func(step string, err error) {
		page.Unavailable = true
		page.UnavailableReason = webui.MsgErrUnavailable
		app.render(writer, request, unavailable(request, step, err), page)
	}

	// An opened task reads only its newest attempts, bounded by the display
	// limit, instead of every attempt in the repository.
	if selected := request.URL.Query().Get("task"); selected != "" {
		task, exists, err := app.Store.Task(request.Context(), stored.ID, selected)
		if err != nil {
			answerUnavailable("task record read", err)
			return
		}
		if exists {
			attempts, more, err := app.Store.RecentCheckAttemptsForTask(request.Context(), stored.ID, task.ID, maximumBrowserTaskAttempts)
			if err != nil {
				answerUnavailable("task attempt read", err)
				return
			}
			detail := &webui.TaskDetail{Task: browserTaskSummary(stored.Address, task), AttemptsTruncated: more}
			for _, attempt := range attempts {
				detail.Attempts = append(detail.Attempts, app.browserAttemptRecord(request.Context(), attempt))
			}
			page.Detail = detail
			app.render(writer, request, http.StatusOK, page)
			return
		}
		page.NotFound = true
	}

	input, problem := parseTaskPageInput(request.URL.Query())
	if problem != "" {
		app.renderError(writer, request, http.StatusBadRequest, webui.MsgErrBadRequest, "")
		return
	}
	views, more, err := app.repositoryTaskPage(request.Context(), stored, input.before, input.limit)
	if err != nil {
		answerUnavailable("task list read", err)
		return
	}
	page.Tasks = app.browserTaskSummaries(request.Context(), views)
	// A count is the whole list only when this page is all of it.
	page.Complete = input.before == nil && !more
	if more {
		page.MoreURL = taskPageMoreURL(page.ListURL, input.limit, views[len(views)-1].task.Cursor())
	}
	if page.NotFound {
		app.render(writer, request, http.StatusNotFound, page)
		return
	}
	app.render(writer, request, http.StatusOK, page)
}

// browserTaskSummaries builds the list rows of views, each with its latest
// attempt.
func (app *App) browserTaskSummaries(ctx context.Context, views []taskView) []webui.TaskSummary {
	summaries := make([]webui.TaskSummary, 0, len(views))
	for _, view := range views {
		summary := browserTaskSummary(view.address, view.task)
		if view.hasLatest {
			summary.Latest = app.browserAttemptRecord(ctx, view.latest)
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func browserCheckConfiguration(configuration state.CheckConfiguration, configured bool) webui.CheckConfigurationView {
	view := webui.CheckConfigurationView{Configured: configured}
	if !configured {
		return view
	}
	view.Version = configuration.Version
	view.RecordedAt = configuration.CreatedAt
	for _, check := range configuration.Checks {
		view.Checks = append(view.Checks, webui.CheckDefinitionLine{Name: check.Name, Command: check.Command})
	}
	return view
}

func browserTaskSummary(address string, task state.Task) webui.TaskSummary {
	return webui.TaskSummary{
		ID:               task.ID,
		ShortID:          shortOpaqueID(task.ID),
		Title:            task.Title,
		Status:           task.Status,
		URL:              tasksURL(address, task.ID),
		CyclesUsed:       task.CorrectionCyclesUsed,
		CyclesLeft:       task.CorrectionCyclesRemaining(),
		CycleLimit:       state.CorrectionCycleLimit,
		CreatedAt:        task.CreatedAt,
		UpdatedAt:        task.UpdatedAt,
		InitialCheckDone: task.InitialCheckDone,
	}
}

func (app *App) browserAttemptRecord(ctx context.Context, attempt state.CheckAttempt) webui.AttemptRecord {
	record := webui.AttemptRecord{
		ID:                   attempt.ID,
		ShortID:              shortOpaqueID(attempt.ID),
		Status:               attempt.Status,
		RevisionOID:          attempt.RevisionOID,
		RevisionShortOID:     shortOID(attempt.RevisionOID),
		WorktreeState:        attempt.EffectiveWorktreeState(),
		ConfigurationVersion: attempt.ConfigurationVersion,
		StartedAt:            attempt.StartedAt,
		DurationMS:           attempt.DurationMS,
		Summary:              attempt.Summary,
		OutputTruncated:      attempt.LogTruncated,
		Protection:           browserProtection(attempt.JobID, attempt.Protection, attempt.ExecutionScope),
		LogError:             attempt.LogError,
		Sequence:             attempt.Sequence,
		CycleID:              attempt.CycleID,
		TimeoutMS:            attempt.TimeoutMS,
		OutputLimitBytes:     attempt.OutputLimitBytes,
		CleanupFailed:        attempt.CleanupFailed(),
	}
	if attempt.Status != state.AttemptPending {
		record.FinishedAt = attempt.FinishedAt
		// Whether the log is kept follows the retention saved now; while
		// that cannot be read, neither can the log.
		record.LogStatus = webui.LogUnavailable
		if retention, err := app.Store.CheckLogRetention(ctx); err == nil {
			record.LogStatus = webui.LogStatusOf(app.Store.CheckLogState(attempt, retention, app.now()))
			if expires := retention.LogExpiry(attempt); expires != nil {
				record.LogExpiresAt = *expires
			}
		}
	}
	record.CredentialProvenance = browserProvenance(attempt.JobID, attempt.CredentialID, attempt.ExecutionScope)
	for _, result := range attempt.Results {
		line := webui.CheckResultLine{
			Name:          result.Name,
			Command:       result.Command,
			Status:        result.Status,
			DurationMS:    result.DurationMS,
			OutputExcerpt: result.OutputExcerpt,
			Truncated:     result.Truncated,
			CleanupError:  result.CleanupError,
		}
		if result.ExitCode != nil {
			line.ExitCode = *result.ExitCode
			line.HasExitCode = true
		}
		record.Results = append(record.Results, line)
	}
	return record
}
