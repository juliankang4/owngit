package server

import (
	"net/http"
	"sort"

	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

const maximumBrowserTaskAttempts = 100

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
	page.ConfiguredChecksURL = configuredChecksURL(stored.ID)

	configuration, configured, err := app.Store.LatestCheckConfiguration(request.Context(), stored.ID)
	if err != nil {
		app.renderError(writer, request, http.StatusServiceUnavailable, webui.MsgTasksUnavail, "")
		return
	}
	page.Configuration = browserCheckConfiguration(configuration, configured)

	tasks, err := app.Store.Tasks(request.Context(), stored.ID)
	if err != nil {
		page.Unavailable = true
		page.UnavailableReason = webui.MsgErrUnavailable
		app.render(writer, http.StatusServiceUnavailable, page)
		return
	}
	unavailable := func() {
		page.Unavailable = true
		page.UnavailableReason = webui.MsgErrUnavailable
		app.render(writer, http.StatusServiceUnavailable, page)
	}

	// An opened task reads only its newest attempts, bounded by the display
	// limit, instead of every attempt in the repository.
	selected := request.URL.Query().Get("task")
	if selected != "" {
		for _, task := range tasks {
			if task.ID != selected {
				continue
			}
			attempts, more, err := app.Store.RecentCheckAttemptsForTask(request.Context(), stored.ID, task.ID, maximumBrowserTaskAttempts)
			if err != nil {
				unavailable()
				return
			}
			detail := &webui.TaskDetail{Task: browserTaskSummary(task), AttemptsTruncated: more}
			for _, attempt := range attempts {
				detail.Attempts = append(detail.Attempts, app.browserAttemptRecord(attempt))
			}
			page.Detail = detail
			app.render(writer, http.StatusOK, page)
			return
		}
		page.NotFound = true
	}

	// Tasks with the newest repository registration appear first. The stored
	// sequence remains the authority and is never replaced with a list index.
	sort.SliceStable(tasks, func(left, right int) bool {
		if tasks[left].LastRegisteredSequence != tasks[right].LastRegisteredSequence {
			return tasks[left].LastRegisteredSequence > tasks[right].LastRegisteredSequence
		}
		if !tasks[left].UpdatedAt.Equal(tasks[right].UpdatedAt) {
			return tasks[left].UpdatedAt.After(tasks[right].UpdatedAt)
		}
		return tasks[left].ID > tasks[right].ID
	})

	// Each list row shows only its task's latest attempt, so the list reads
	// one attempt per displayed task.
	for _, task := range tasks {
		summaryView := browserTaskSummary(task)
		latest, exists, err := app.Store.LatestCheckAttemptForTask(request.Context(), stored.ID, task.ID)
		if err != nil {
			page.Tasks = nil
			unavailable()
			return
		}
		if exists {
			summaryView.Latest = app.browserAttemptRecord(latest)
		}
		page.Tasks = append(page.Tasks, summaryView)
	}
	if page.NotFound {
		app.render(writer, http.StatusNotFound, page)
		return
	}
	app.render(writer, http.StatusOK, page)
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

func browserTaskSummary(task state.Task) webui.TaskSummary {
	return webui.TaskSummary{
		ID:               task.ID,
		ShortID:          shortOpaqueID(task.ID),
		Title:            task.Title,
		Status:           task.Status,
		URL:              tasksURL(task.RepositoryID, task.ID),
		CyclesUsed:       task.CorrectionCyclesUsed,
		CyclesLeft:       task.CorrectionCyclesRemaining(),
		CycleLimit:       state.CorrectionCycleLimit,
		CreatedAt:        task.CreatedAt,
		UpdatedAt:        task.UpdatedAt,
		InitialCheckDone: task.InitialCheckDone,
	}
}

func (app *App) browserAttemptRecord(attempt state.CheckAttempt) webui.AttemptRecord {
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
		record.LogStatus = webui.LogStatusOf(app.Store.CheckLogState(attempt.LogID, attempt.LogExpiresAt, app.now()))
		if attempt.LogExpiresAt != nil {
			record.LogExpiresAt = *attempt.LogExpiresAt
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
