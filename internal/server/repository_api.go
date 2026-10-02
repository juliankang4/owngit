package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

const (
	repositoryCollectionAPIPath = "/api/v1/repositories"
	// maximumListedRepositories bounds one repository list response. A list
	// that stops early says so with truncated.
	maximumListedRepositories = 1000
)

// repositoryAPIItem describes one repository in the repository API.
type repositoryAPIItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Address is where the repository answers: its current name, or its ID
	// when it was never renamed.
	Address     string    `json:"address"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	CloneURL    string    `json:"clone_url"`
	// DefaultBranch is shown by a single repository read when its refs can be
	// read now. It is left out of lists, which read no Git data.
	DefaultBranch string `json:"default_branch,omitempty"`
	// DefaultBranchError replaces DefaultBranch when the branches could not
	// be read now, so an unreadable repository is never shown as one without
	// a default branch.
	DefaultBranchError string `json:"default_branch_error,omitempty"`
	// PushRefNamespaces, shown by a single repository read, are the ref
	// namespaces a push may change: branches, tags and the repository's
	// extra ref namespaces. PushRefNamespacesError replaces them when the
	// saved extra namespaces cannot be read, which refuses every push.
	PushRefNamespaces      []string `json:"push_ref_namespaces,omitempty"`
	PushRefNamespacesError string   `json:"push_ref_namespaces_error,omitempty"`
	// Aliases are the earlier addresses that still redirect to Address,
	// shown by a single repository read.
	Aliases []repositoryAliasItem `json:"aliases,omitempty"`
}

// repositoryAliasItem is an earlier address of a renamed repository.
type repositoryAliasItem struct {
	Name  string    `json:"name"`
	Until time.Time `json:"until"`
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
func (app *App) handleRepositoryAPI(writer http.ResponseWriter, request *http.Request, settings state.Settings, one bool) {
	if !app.authorizeAPI(writer, request, settings) {
		return
	}
	if one {
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		address, _ := repositoryAddressOf(request)
		app.showRepositoryAPI(writer, request, address.id)
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
		writeAPIError(writer, unavailable(request, "repository list read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
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
		writeAPIError(writer, unavailable(request, "repository record read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	}
	if !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return
	}
	item := app.repositoryAPIItem(request, stored)
	aliases, err := app.Store.RepositoryAliases(request.Context(), stored.ID, app.now())
	if err != nil {
		writeAPIError(writer, unavailable(request, "repository alias read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	}
	for _, alias := range aliases {
		item.Aliases = append(item.Aliases, repositoryAliasItem{Name: alias.Name, Until: alias.AliasUntil.UTC()})
	}
	// A repository that is being prepared or held by another Git operation
	// is still described; only its default branch is left out. A branch read
	// that failed is named as such, so it is never shown as a repository
	// without a default branch.
	switch snapshot, err := app.Repositories.RefSnapshotWithin(request.Context(), stored.ID, repositoryListWait); {
	case err == nil && !snapshot.Stale:
		item.DefaultBranch = snapshot.Summary.DefaultBranch
	case err == nil:
		// The last snapshot read is stale: a writer may have changed the refs
		// since, so the branch is left out like a busy repository's.
	case errors.Is(err, repository.ErrRepositoryBusy) || errors.Is(err, repository.ErrRepositoryPreparing):
		// Intended fallback: the repository is being written or prepared.
	case request.Context().Err() != nil:
		// The client left; the answer it would have read is moot.
	case errors.Is(err, repository.ErrRepositoryNotFound):
		// The repository was deleted while this request ran.
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return
	case errors.Is(err, repository.ErrStorageUnavailable):
		logFailure(request, "repository ref read", err)
		item.DefaultBranchError = "The repository folder is missing or unusable; see the server log."
	default:
		logFailure(request, "repository ref read", err)
		item.DefaultBranchError = "OwnGit could not read the branches; see the server log."
	}
	extra, err := app.Store.RepositoryExtraRefPrefixes(request.Context(), stored.ID)
	var policyErr *state.PolicyError
	switch {
	case errors.As(err, &policyErr):
		logFailure(request, "repository ref namespaces read", err)
		item.PushRefNamespacesError = policyErr.Advice()
	case err != nil:
		writeAPIError(writer, unavailable(request, "repository ref namespaces read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	default:
		item.PushRefNamespaces = append([]string{"refs/heads/", "refs/tags/"}, extra...)
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
		case errors.Is(err, repository.ErrFailedCreationLimit):
			writeAPIError(writer, unavailable(request, "repository creation", err), "repository_create_kept", webui.Text(webui.LangEN, webui.MsgRepoCreationKept)+" ("+strings.ToLower(name)+".git)", nil)
		case errors.Is(err, repository.ErrStorageInUse):
			logFailure(request, "repository creation", err)
			writeAPIError(writer, http.StatusConflict, "repository_storage_in_use", webui.Text(webui.LangEN, webui.MsgSetupStorageInUse), nil)
		case errors.Is(err, repository.ErrImportInProgress):
			writeAPIError(writer, http.StatusConflict, "repository_name_busy", "An import for this name is still running or needs recovery. Try again after it finishes.", nil)
		case errors.Is(err, repository.ErrFolderExists):
			writeAPIError(writer, http.StatusConflict, "repository_exists", "A folder already exists for this name. OwnGit has not adopted or removed it. Choose another name, or move the folder aside after checking its contents.", nil)
		case errors.Is(err, repository.ErrNameTaken):
			writeAPIError(writer, http.StatusConflict, "repository_exists", "The repository name is already in use.", nil)
		case errors.Is(err, repository.ErrReservedName):
			writeAPIError(writer, http.StatusUnprocessableEntity, "reserved_repository_name", "The repository name is reserved for a form page. Choose another name.", nil)
		case errors.Is(err, repository.ErrInvalidName):
			writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_repository_name", "Use 1 to 100 letters, numbers, dots, underscores, or hyphens, starting with a letter or number. The name cannot end in .git or be a Windows device name such as CON.", nil)
		case errors.Is(err, repository.ErrInvalidDescription):
			writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_repository_description", "The description can be at most 500 bytes.", nil)
		case errors.As(err, new(*state.PolicyError)):
			writeSettingUnreadable(writer, request, "repository creation", err)
		default:
			writeAPIError(writer, unavailable(request, "repository creation", err), "repository_create_failed", webui.Text(webui.LangEN, webui.MsgRepoCreateFail), nil)
		}
		return
	}
	writeAPIJSON(writer, http.StatusCreated, repositoryResponse{OK: true, Repository: app.repositoryAPIItem(request, created)})
}

func (app *App) repositoryAPIItem(request *http.Request, stored state.Repository) repositoryAPIItem {
	return repositoryAPIItem{
		// The store keeps whole seconds, so a new repository reports the same
		// time as later reads of it.
		ID: stored.ID, Name: stored.Name, Address: stored.Address, Description: stored.Description, CreatedAt: stored.CreatedAt.UTC().Truncate(time.Second),
		CloneURL: app.cloneURL(request, stored.Address),
	}
}

type renameRepositoryInput struct {
	Name string `json:"name"`
}

// handleRenameRepositoryAPI answers POST /api/v1/repositories/{name}/rename
// with the administrator password. The repository then answers at the new
// name, and its earlier address redirects there for 90 days.
func (app *App) handleRenameRepositoryAPI(writer http.ResponseWriter, request *http.Request, repositoryID, remainder string) {
	if remainder != "" {
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	}
	if request.Method != http.MethodPost {
		writeAPIMethodError(writer, http.MethodPost)
		return
	}
	if !app.authorizeAdminAPI(writer, request) {
		return
	}
	var input renameRepositoryInput
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	renamed, err := app.renameRepository(request, repositoryID, input.Name)
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrRepositoryNotFound):
			writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		case errors.Is(err, repository.ErrReservedName):
			writeAPIError(writer, http.StatusUnprocessableEntity, "reserved_repository_name", "The repository name is reserved for a form page. Choose another name.", nil)
		case errors.Is(err, repository.ErrInvalidName):
			writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_repository_name", "Use 1 to 100 letters, numbers, dots, underscores, or hyphens, starting with a letter or number. The name cannot end in .git or be a Windows device name such as CON.", nil)
		case errors.Is(err, repository.ErrNameTaken):
			writeAPIError(writer, http.StatusConflict, "repository_name_taken", "Another repository uses this name, as its name, its ID or an earlier name that still redirects to it.", nil)
		case errors.Is(err, repository.ErrRepositoryBusy):
			code, _ := busyNotice(err)
			writeAPIError(writer, http.StatusConflict, "repository_busy", "The repository was not renamed. "+webui.Text(webui.LangEN, code), nil)
		default:
			writeAPIError(writer, unavailable(request, "repository rename", err), "repository_rename_failed", "The repository could not be renamed.", nil)
		}
		return
	}
	writeAPIJSON(writer, http.StatusOK, repositoryResponse{OK: true, Repository: app.repositoryAPIItem(request, renamed)})
}

// renameRepository renames repository id for the dashboard and the API. A
// background activity count holds the repository's read lock for as long as
// its history walk takes, so counting pauses for the rename.
func (app *App) renameRepository(request *http.Request, id, name string) (state.Repository, error) {
	defer app.activity.pause(id, true)()
	return app.Repositories.Rename(request.Context(), id, name, app.now())
}
