package server

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"owngit/internal/checkapi"
	"owngit/internal/state"
)

// The task views show check tasks as the dashboard does, with general
// access: a repository's tasks on its Tasks page, one task with its newest
// attempts, and the most recently updated tasks of every repository on the
// Coding tools page. The task API routes give the same reads as JSON. The
// check helper's own task routes, which need a helper credential, are in
// checks.go.

const (
	// maximumBrowserTaskAttempts bounds the attempts one opened task shows.
	maximumBrowserTaskAttempts = 100
	// maximumRecentTasks bounds the tasks across repositories that the
	// Coding tools page and the recent task list show.
	maximumRecentTasks = 10

	taskViewAPIPath = "/api/v1/tasks"
)

// taskView is one listed task with its latest attempt, when it has one,
// and the current address of the task's repository.
type taskView struct {
	task      state.Task
	address   string
	latest    state.CheckAttempt
	hasLatest bool
}

// repositoryTaskViews lists a repository's tasks, those with the newest
// registered attempt first. The stored sequence stays the authority and is
// never replaced with a list index.
func (app *App) repositoryTaskViews(ctx context.Context, stored state.Repository) ([]taskView, error) {
	tasks, err := app.Store.Tasks(ctx, stored.ID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(tasks, func(left, right int) bool {
		if tasks[left].LastRegisteredSequence != tasks[right].LastRegisteredSequence {
			return tasks[left].LastRegisteredSequence > tasks[right].LastRegisteredSequence
		}
		if !tasks[left].UpdatedAt.Equal(tasks[right].UpdatedAt) {
			return tasks[left].UpdatedAt.After(tasks[right].UpdatedAt)
		}
		return tasks[left].ID > tasks[right].ID
	})
	return app.withLatestAttempts(ctx, tasks, map[string]string{stored.ID: stored.Address})
}

// recentTaskViews lists the tasks of repositories with the newest change
// first, at most maximumRecentTasks, and whether more exist. Attempt
// sequences count within one repository, so across repositories time
// orders: when the latest attempt was registered, or the task itself last
// changed, whichever is later. Both are the server's own times.
func (app *App) recentTaskViews(ctx context.Context, repositories []state.Repository) ([]taskView, bool, error) {
	var tasks []state.Task
	addresses := make(map[string]string, len(repositories))
	for _, stored := range repositories {
		addresses[stored.ID] = stored.Address
		listed, err := app.Store.Tasks(ctx, stored.ID)
		if err != nil {
			return nil, false, err
		}
		tasks = append(tasks, listed...)
	}
	views, err := app.withLatestAttempts(ctx, tasks, addresses)
	if err != nil {
		return nil, false, err
	}
	changed := func(view taskView) time.Time {
		if view.hasLatest && view.latest.CreatedAt.After(view.task.UpdatedAt) {
			return view.latest.CreatedAt
		}
		return view.task.UpdatedAt
	}
	sort.SliceStable(views, func(left, right int) bool {
		leftChanged, rightChanged := changed(views[left]), changed(views[right])
		if !leftChanged.Equal(rightChanged) {
			return leftChanged.After(rightChanged)
		}
		if views[left].task.RepositoryID != views[right].task.RepositoryID {
			return views[left].task.RepositoryID < views[right].task.RepositoryID
		}
		return views[left].task.ID > views[right].task.ID
	})
	more := len(views) > maximumRecentTasks
	if more {
		views = views[:maximumRecentTasks]
	}
	return views, more, nil
}

// withLatestAttempts reads the latest attempt of each task, one read per
// listed task. addresses maps each repository ID to its current address.
func (app *App) withLatestAttempts(ctx context.Context, tasks []state.Task, addresses map[string]string) ([]taskView, error) {
	views := make([]taskView, 0, len(tasks))
	for _, task := range tasks {
		latest, exists, err := app.Store.LatestCheckAttemptForTask(ctx, task.RepositoryID, task.ID)
		if err != nil {
			return nil, err
		}
		views = append(views, taskView{task: task, address: addresses[task.RepositoryID], latest: latest, hasLatest: exists})
	}
	return views, nil
}

// taskViewJSON is one listed task and its latest attempt, in the fields the
// check helper's task and attempt records use.
type taskViewJSON struct {
	*checkapi.Task
	// RepositoryAddress is where the task's repository answers now.
	RepositoryAddress string            `json:"repository_address"`
	LatestAttempt     *checkapi.Attempt `json:"latest_attempt"`
}

type taskViewListResponse struct {
	OK    bool           `json:"ok"`
	Tasks []taskViewJSON `json:"tasks"`
	// Truncated is true when the recent task list left out older tasks.
	Truncated bool `json:"truncated"`
}

type taskDetailResponse struct {
	OK       bool                `json:"ok"`
	Task     *checkapi.Task      `json:"task"`
	Attempts []*checkapi.Attempt `json:"attempts"`
	// AttemptsTruncated is true when older attempts were left out.
	AttemptsTruncated bool `json:"attempts_truncated"`
}

func (app *App) taskViewsJSON(request *http.Request, views []taskView) []taskViewJSON {
	items := make([]taskViewJSON, 0, len(views))
	for _, view := range views {
		item := taskViewJSON{Task: taskJSON(view.task), RepositoryAddress: view.address}
		if view.hasLatest {
			item.LatestAttempt = app.attemptJSON(request, view.latest)
		}
		items = append(items, item)
	}
	return items
}

// handleTaskViewAPI answers the task views with general access:
// GET /api/v1/tasks lists the recent tasks of every repository,
// GET /api/v1/tasks/REPOSITORY a repository's tasks, and
// GET /api/v1/tasks/REPOSITORY/TASK one task with its newest attempts.
func (app *App) handleTaskViewAPI(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	if !app.authorizeAPI(writer, request, settings) {
		return
	}
	if request.Method != http.MethodGet {
		writeAPIMethodError(writer, http.MethodGet)
		return
	}
	ctx := request.Context()
	rest := strings.TrimPrefix(strings.TrimPrefix(request.URL.Path, taskViewAPIPath), "/")
	if rest == "" {
		repositories, err := app.visibleRepositories(request)
		if err != nil {
			writeAPIError(writer, unavailable(request, "repository list read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
			return
		}
		views, more, err := app.recentTaskViews(ctx, repositories)
		if err != nil {
			writeAPIError(writer, unavailable(request, "recent task read", err), "state_unavailable", "Task records could not be read.", nil)
			return
		}
		writeAPIJSON(writer, http.StatusOK, taskViewListResponse{OK: true, Tasks: app.taskViewsJSON(request, views), Truncated: more})
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	}
	// The repository name was resolved with every repository path (see
	// resolveRepositoryAddress), and authorizeAPI answered one that reaches
	// no current repository.
	address, _ := repositoryAddressOf(request)
	repositoryID := address.id
	stored, exists, err := app.visibleRepository(request, repositoryID)
	if err != nil {
		writeAPIError(writer, unavailable(request, "repository record read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	}
	if !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return
	}
	if len(parts) == 1 {
		views, err := app.repositoryTaskViews(ctx, stored)
		if err != nil {
			writeAPIError(writer, unavailable(request, "task list read", err), "state_unavailable", "Task records could not be read.", nil)
			return
		}
		writeAPIJSON(writer, http.StatusOK, taskViewListResponse{OK: true, Tasks: app.taskViewsJSON(request, views)})
		return
	}
	task, exists, err := app.Store.Task(ctx, repositoryID, parts[1])
	if err != nil {
		writeAPIError(writer, unavailable(request, "task record read", err), "state_unavailable", "The task record could not be read.", nil)
		return
	}
	if !exists {
		writeAPIError(writer, http.StatusNotFound, "task_not_found", "The task does not exist.", nil)
		return
	}
	attempts, more, err := app.Store.RecentCheckAttemptsForTask(ctx, repositoryID, task.ID, maximumBrowserTaskAttempts)
	if err != nil {
		writeAPIError(writer, unavailable(request, "task attempt read", err), "state_unavailable", "The task's attempts could not be read.", nil)
		return
	}
	response := taskDetailResponse{OK: true, Task: taskJSON(task), Attempts: []*checkapi.Attempt{}, AttemptsTruncated: more}
	for _, attempt := range attempts {
		response.Attempts = append(response.Attempts, app.attemptJSON(request, attempt))
	}
	writeAPIJSON(writer, http.StatusOK, response)
}
