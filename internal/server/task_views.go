package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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
	// defaultTaskPageSize bounds one page of a repository's tasks, and
	// maximumTaskPageSize is the largest page a caller may ask for.
	defaultTaskPageSize = 50
	maximumTaskPageSize = 100

	taskViewAPIPath = "/api/v1/tasks"
)

// taskView is one listed task with its latest attempt, when it has one,
// and the current address of the task's repository.
type taskView struct {
	task      state.Task
	address   string
	latest    state.CheckAttempt
	hasLatest bool
	evidence  state.RevisionCheckEvidence
}

// taskPageInput is the paging of one repository task list.
type taskPageInput struct {
	limit  int
	before *state.TaskCursor
}

// parseTaskPageInput reads the limit and before parameters of a task list. An
// invalid value returns the API problem code that names it.
func parseTaskPageInput(query url.Values) (taskPageInput, string) {
	input := taskPageInput{limit: defaultTaskPageSize}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > maximumTaskPageSize {
			return taskPageInput{}, "invalid_list_limit"
		}
		input.limit = limit
	}
	if value := query.Get("before"); value != "" {
		cursor, ok := state.ParseTaskCursor(value)
		if !ok {
			return taskPageInput{}, "invalid_list_before"
		}
		input.before = &cursor
	}
	return input, ""
}

// taskPageProblemText is the sentence that goes with one invalid task page
// parameter.
func taskPageProblemText(code string) string {
	if code == "invalid_list_limit" {
		return fmt.Sprintf("The page size must be a whole number between 1 and %d.", maximumTaskPageSize)
	}
	return "The continuation is not a task position."
}

// repositoryTaskPage lists one page of a repository's tasks, those with the
// newest registered attempt first, then update time, then ID, and reports
// whether older tasks follow. The stored sequence stays the authority and is
// never replaced with a list index.
func (app *App) repositoryTaskPage(ctx context.Context, stored state.Repository, before *state.TaskCursor, limit int) ([]taskView, bool, error) {
	tasks, more, err := app.Store.TaskPage(ctx, stored.ID, before, limit)
	if err != nil {
		return nil, false, err
	}
	views, err := app.withLatestAttempts(ctx, tasks, map[string]string{stored.ID: stored.Address})
	if err != nil {
		return nil, false, err
	}
	return views, more, nil
}

// recentTaskViews lists the tasks of repositories with the newest change
// first, at most maximumRecentTasks, and whether more exist. The change time
// is the later of the latest attempt's registration and the task's own update,
// both the server's own times. The state read keeps only the newest page, so
// the view loads attempts for the tasks it shows alone.
func (app *App) recentTaskViews(ctx context.Context, repositories []state.Repository) ([]taskView, bool, error) {
	ids := make([]string, 0, len(repositories))
	addresses := make(map[string]string, len(repositories))
	for _, stored := range repositories {
		ids = append(ids, stored.ID)
		addresses[stored.ID] = stored.Address
	}
	tasks, more, err := app.Store.RecentTasks(ctx, ids, maximumRecentTasks)
	if err != nil {
		return nil, false, err
	}
	views, err := app.withLatestAttempts(ctx, tasks, addresses)
	if err != nil {
		return nil, false, err
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
		evidence, err := app.Store.TaskRevisionEvidence(ctx, task.RepositoryID, task.ID)
		if err != nil {
			return nil, err
		}
		views = append(views, taskView{task: task, address: addresses[task.RepositoryID], latest: latest, hasLatest: exists, evidence: evidence})
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
	// Next continues a repository's task list below the last task shown.
	// It is absent on the last page.
	Next string `json:"next,omitempty"`
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
		item.Evidence = &view.evidence
		if view.hasLatest {
			item.LatestAttempt = app.attemptJSON(request, view.latest)
		}
		items = append(items, item)
	}
	return items
}

// taskPageMoreURL is the next page's address of a task list, keeping the page
// size the caller read. It works without JavaScript.
func taskPageMoreURL(listURL string, limit int, cursor state.TaskCursor) string {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(limit))
	query.Set("before", cursor.String())
	return listURL + "?" + query.Encode()
}

// taskViewQueryAllowed accepts the paging parameters of one repository's task
// list, each given once. The recent list and one task take no parameters.
func taskViewQueryAllowed(request *http.Request) bool {
	if request.Method != http.MethodGet {
		return false
	}
	rest, found := strings.CutPrefix(request.URL.Path, taskViewAPIPath+"/")
	if !found || rest == "" || strings.Contains(rest, "/") {
		return false
	}
	for key, values := range request.URL.Query() {
		if (key != "limit" && key != "before") || len(values) != 1 {
			return false
		}
	}
	return true
}

// handleTaskViewAPI answers the task views with general access:
// GET /api/v1/tasks lists the recent tasks of every repository,
// GET /api/v1/tasks/REPOSITORY one page of a repository's tasks, and
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
		input, problem := parseTaskPageInput(request.URL.Query())
		if problem != "" {
			writeAPIError(writer, http.StatusBadRequest, problem, taskPageProblemText(problem), nil)
			return
		}
		views, more, err := app.repositoryTaskPage(ctx, stored, input.before, input.limit)
		if err != nil {
			writeAPIError(writer, unavailable(request, "task list read", err), "state_unavailable", "Task records could not be read.", nil)
			return
		}
		response := taskViewListResponse{OK: true, Tasks: app.taskViewsJSON(request, views)}
		if more {
			response.Next = views[len(views)-1].task.Cursor().String()
		}
		writeAPIJSON(writer, http.StatusOK, response)
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
	item, err := app.taskEvidenceJSON(request, task)
	if err != nil {
		writeAPIError(writer, unavailable(request, "task evidence read", err), "state_unavailable", "Task evidence could not be read.", nil)
		return
	}
	response := taskDetailResponse{OK: true, Task: item, Attempts: []*checkapi.Attempt{}, AttemptsTruncated: more}
	for _, attempt := range attempts {
		response.Attempts = append(response.Attempts, app.attemptJSON(request, attempt))
	}
	writeAPIJSON(writer, http.StatusOK, response)
}
