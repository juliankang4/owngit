package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

const (
	repositoryCollectionAPIPath = "/api/v1/repositories"
	// maximumListedRepositories bounds one repository list response. A list
	// that stops early says so with truncated.
	maximumListedRepositories = 1000
)

// repositoryAPIItem describes one repository in the repository API.
type repositoryAPIItem struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	CloneURL    string    `json:"clone_url"`
	// DefaultBranch is shown by a single repository read when its refs can be
	// read now. It is left out of lists, which read no Git data.
	DefaultBranch string `json:"default_branch,omitempty"`
}

type repositoryListResponse struct {
	OK           bool                `json:"ok"`
	Repositories []repositoryAPIItem `json:"repositories"`
	Truncated    bool                `json:"truncated"`
}

type repositoryResponse struct {
	OK         bool              `json:"ok"`
	Repository repositoryAPIItem `json:"repository"`
}

type createRepositoryInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// repositoryAPIRoute reports whether requestPath addresses the repository
// collection or one repository, and returns the repository ID for the latter.
func repositoryAPIRoute(requestPath string) (id string, ok bool) {
	if requestPath == repositoryCollectionAPIPath {
		return "", true
	}
	id, found := strings.CutPrefix(requestPath, repositoryCollectionAPIPath+"/")
	if !found || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// visibleRepositories returns the repositories a request may see, and
// visibleRepository one of them. Every repository list goes through these
// two functions, so a later per-caller filter has one place to live. Today
// general access sees every repository.
func (app *App) visibleRepositories(request *http.Request) ([]state.Repository, error) {
	return app.Store.Repositories(request.Context())
}

func (app *App) visibleRepository(request *http.Request, id string) (state.Repository, bool, error) {
	return app.Store.Repository(request.Context(), id)
}

// handleRepositoryAPI lists, shows, and creates repositories with the same
// general-access authorization as the other general-access API routes.
func (app *App) handleRepositoryAPI(writer http.ResponseWriter, request *http.Request, settings state.Settings, id string) {
	if !app.authorizeAPI(writer, request, settings) {
		return
	}
	if id != "" {
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		app.showRepositoryAPI(writer, request, id)
		return
	}
	switch request.Method {
	case http.MethodGet:
		app.listRepositoriesAPI(writer, request)
	case http.MethodPost:
		app.createRepositoryAPI(writer, request)
	default:
		writeAPIMethodError(writer, http.MethodGet+", "+http.MethodPost)
	}
}

func (app *App) listRepositoriesAPI(writer http.ResponseWriter, request *http.Request) {
	repositories, err := app.visibleRepositories(request)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	}
	response := repositoryListResponse{OK: true, Repositories: []repositoryAPIItem{}}
	if len(repositories) > maximumListedRepositories {
		repositories, response.Truncated = repositories[:maximumListedRepositories], true
	}
	for _, stored := range repositories {
		response.Repositories = append(response.Repositories, app.repositoryAPIItem(request, stored))
	}
	writeAPIJSON(writer, http.StatusOK, response)
}

func (app *App) showRepositoryAPI(writer http.ResponseWriter, request *http.Request, id string) {
	stored, exists, err := app.visibleRepository(request, id)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	}
	if !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return
	}
	item := app.repositoryAPIItem(request, stored)
	// A repository that is being prepared or held by another Git operation
	// is still described; only its default branch is left out.
	if snapshot, err := app.Repositories.RefSnapshotWithin(request.Context(), stored.ID, repositoryListWait); err == nil && !snapshot.Stale {
		item.DefaultBranch = snapshot.Summary.DefaultBranch
	}
	writeAPIJSON(writer, http.StatusOK, repositoryResponse{OK: true, Repository: item})
}

// createRepositoryAPI applies the same name and description rules as the
// browser form. JSON is required, like every mutating API request.
func (app *App) createRepositoryAPI(writer http.ResponseWriter, request *http.Request) {
	var input createRepositoryInput
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	description := strings.TrimSpace(input.Description)
	created, err := app.Repositories.Create(request.Context(), name, description)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrImportInProgress):
			writeAPIError(writer, http.StatusConflict, "repository_name_busy", "An import for this name is still running or needs recovery. Try again after it finishes.", nil)
		case errors.Is(err, repository.ErrNameTaken):
			writeAPIError(writer, http.StatusConflict, "repository_exists", "The repository name is already in use.", nil)
		case errors.Is(err, repository.ErrReservedName):
			writeAPIError(writer, http.StatusUnprocessableEntity, "reserved_repository_name", "The repository name is reserved for a form page. Choose another name.", nil)
		case errors.Is(err, repository.ErrInvalidName):
			writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_repository_name", "Use 1 to 100 letters, numbers, dots, underscores, or hyphens, starting with a letter or number and not ending in .git.", nil)
		case errors.Is(err, repository.ErrInvalidDescription):
			writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_repository_description", "The description can be at most 500 bytes.", nil)
		default:
			writeAPIError(writer, http.StatusServiceUnavailable, "repository_create_failed", "The repository could not be created.", nil)
		}
		return
	}
	writeAPIJSON(writer, http.StatusCreated, repositoryResponse{OK: true, Repository: app.repositoryAPIItem(request, created)})
}

func (app *App) repositoryAPIItem(request *http.Request, stored state.Repository) repositoryAPIItem {
	return repositoryAPIItem{
		// The store keeps whole seconds, so a new repository reports the same
		// time as later reads of it.
		ID: stored.ID, Name: stored.Name, Description: stored.Description, CreatedAt: stored.CreatedAt.UTC().Truncate(time.Second),
		CloneURL: app.cloneURL(request, stored.ID),
	}
}
