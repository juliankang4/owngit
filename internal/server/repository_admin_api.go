package server

import (
	"errors"
	"net/http"
	"strings"

	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/webui"
)

// The owner API for what the repository Settings tab and its delete page
// change: POST /api/v1/repositories/{id}/default-branch and POST
// /api/v1/repositories/{id}/delete. Both need the administrator password and
// go through the same functions as the pages, with the same checks.

// handleRepositoryAdminAPI answers the default branch and delete routes of
// repository id.
func (app *App) handleRepositoryAdminAPI(writer http.ResponseWriter, request *http.Request, id, resource, remainder string) {
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
	stored, exists, err := app.Store.Repository(request.Context(), id)
	if err != nil {
		writeAPIError(writer, unavailable(request, "repository record read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	}
	if !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return
	}
	if resource == "default-branch" {
		app.defaultBranchAPI(writer, request, id)
		return
	}
	var input struct {
		// Mode is "keep_files" or "delete_files", as on the delete page.
		Mode        string `json:"mode"`
		ConfirmName string `json:"confirm_name"`
	}
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	if input.Mode != webui.DeleteModeKeepFiles && input.Mode != webui.DeleteModeDeleteFiles {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request", "mode must be keep_files or delete_files.", nil)
		return
	}
	rule, err := app.deleteNameRule(request)
	if err != nil {
		writeAPIError(writer, unavailable(request, "delete setting read", err), "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	}
	if rule.Unreadable {
		writeAPIError(writer, http.StatusConflict, "setting_unreadable", webui.Text(webui.LangEN, webui.MsgDeleteNameUnreadableDelete), map[string]string{"setting": "delete_requires_name"})
		return
	}
	if !deleteNameConfirmed(rule, stored.Name, input.ConfirmName) {
		writeAPIError(writer, http.StatusUnprocessableEntity, "name_mismatch", "Settings ask for the repository name before a deletion. Repeat it to confirm: "+stored.Name+".", nil)
		return
	}
	result, incomplete, err := app.deleteRepository(request.Context(), id, repository.DeleteMode(input.Mode))
	if err != nil && !incomplete {
		code, status := app.deleteFailure(request, id, err)
		errorCode := "delete_failed"
		switch status {
		case http.StatusConflict:
			errorCode = "repository_busy"
			if code == webui.MsgStorageChanged {
				errorCode = "repository_storage_changed"
			}
		case http.StatusNotFound:
			errorCode = "repository_not_found"
		}
		writeAPIError(writer, status, errorCode, webui.Text(webui.LangEN, code), nil)
		return
	}
	if incomplete {
		logFailure(request, "repository file removal", err)
	}
	message := ""
	if result.FolderMissing {
		message = webui.Text(webui.LangEN, webui.MsgRepoRemovedMissing)
	}
	// Incomplete is true when the repository is gone from OwnGit but its
	// files are removed or moved at the next start, as the dashboard says.
	writeAPIJSON(writer, http.StatusOK, struct {
		OK            bool   `json:"ok"`
		Repository    string `json:"repository"`
		Mode          string `json:"mode"`
		KeptPath      string `json:"kept_path,omitempty"`
		Incomplete    bool   `json:"incomplete,omitempty"`
		FolderMissing bool   `json:"folder_missing,omitempty"`
		Message       string `json:"message,omitempty"`
	}{true, id, input.Mode, result.KeptPath, incomplete, result.FolderMissing, message})
}

// defaultBranchAPI makes {"branch": name}, an existing branch, the default
// branch of repository id.
func (app *App) defaultBranchAPI(writer http.ResponseWriter, request *http.Request, id string) {
	var input struct {
		Branch string `json:"branch"`
	}
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	full, err := app.setDefaultBranch(request.Context(), id, input.Branch, false)
	var ambiguous *repository.AmbiguousBranchError
	switch {
	case err == nil:
		writeAPIJSON(writer, http.StatusOK, struct {
			OK            bool   `json:"ok"`
			Repository    string `json:"repository"`
			DefaultBranch string `json:"default_branch"`
		}{true, id, strings.TrimPrefix(full, "refs/heads/")})
	case errors.As(err, &ambiguous):
		problem := pullrequest.NewProblem("ambiguous_branch", ambiguous.Error())
		writeAPIError(writer, apiStatus(request, "default branch change", problem), problem.Code, problem.Message, nil)
	case errors.Is(err, repository.ErrBranchNotFound):
		writeAPIError(writer, http.StatusUnprocessableEntity, "branch_not_found", "That branch does not exist in this repository. Nothing was changed.", nil)
	case errors.Is(err, repository.ErrRepositoryBusy):
		code, _ := busyNotice(err)
		writeAPIError(writer, http.StatusConflict, "repository_busy", webui.Text(webui.LangEN, code), nil)
	case errors.Is(err, repository.ErrRepositoryNotFound):
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
	default:
		writeAPIError(writer, unavailable(request, "default branch change", err), "default_branch_failed", webui.Text(webui.LangEN, webui.MsgRepoDefaultBranchFailed), nil)
	}
}
