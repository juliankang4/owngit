package server

import (
	"context"
	"errors"
	"net/http"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// A repository's kept history choice and default branch protection. The
// repository Settings tab and the owner API (/api/v1/repositories/{id}/
// settings) save them through saveRefPolicy, so both warn the same way when
// a change turns one of them off.

// saveRefPolicy saves change for repository id and returns the result
// with a warning for each protection the change turned off: history that
// stops being kept, and a default branch that stops being protected. The
// state decides both in the transaction that saves the change.
func (app *App) saveRefPolicy(ctx context.Context, id string, change state.RepositoryRefPolicyChange) (state.RefPolicySave, []webui.MessageCode, error) {
	saved, err := app.Store.SaveRepositoryRefPolicy(ctx, id, change)
	if err != nil {
		return state.RefPolicySave{}, nil, err
	}
	var warnings []webui.MessageCode
	if saved.KeptHistoryOff {
		warnings = append(warnings, webui.MsgRepoHistoryKeptOff)
	}
	if saved.ProtectionOff {
		warnings = append(warnings, webui.MsgRepoHistoryProtectOff)
	}
	return saved, warnings, nil
}

// repositorySettingsJSON is the owner API's view of a repository's own
// choices.
type repositorySettingsJSON struct {
	// KeptHistory is the repository's choice: "default" follows the server
	// setting, "on" and "off" override it.
	KeptHistory *string `json:"kept_history,omitempty"`
	// KeptHistoryNow is what the repository does now, "on" or "off". Only
	// answers carry it.
	KeptHistoryNow string `json:"kept_history_now,omitempty"`
	// ProtectDefaultBranch refuses pushes that rewrite or delete the
	// default branch.
	ProtectDefaultBranch *bool `json:"protect_default_branch,omitempty"`
}

type repositorySettingsResponse struct {
	OK         bool                   `json:"ok"`
	Repository string                 `json:"repository"`
	Settings   repositorySettingsJSON `json:"settings"`
	// Warnings say what a PATCH turned off allows now.
	Warnings []string `json:"warnings,omitempty"`
}

// handleRepositorySettingsAPI answers GET and PATCH
// /api/v1/repositories/{id}/settings with the administrator password, as
// every administrator API request needs. A PATCH names what it changes and
// answers with the choices as saved. A choice that cannot be read answers
// 409 setting_unreadable; a PATCH that names both choices replaces an
// unreadable row. A PATCH whose result would follow a server default that
// cannot be read is refused and saves nothing.
func (app *App) handleRepositorySettingsAPI(writer http.ResponseWriter, request *http.Request, repositoryID, remainder string) {
	if remainder != "" {
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodPatch {
		writeAPIMethodError(writer, "GET, PATCH")
		return
	}
	if !app.authorizeAdminAPI(writer, request) {
		return
	}
	if !app.importRepositoryExists(writer, request, repositoryID) {
		return
	}
	response := repositorySettingsResponse{OK: true, Repository: repositoryID}
	if request.Method == http.MethodPatch {
		var input repositorySettingsJSON
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		if input.KeptHistoryNow != "" {
			writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "kept_history_now is not a setting; set kept_history.", nil)
			return
		}
		if input.KeptHistory == nil && input.ProtectDefaultBranch == nil {
			writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "Name kept_history, protect_default_branch or both.", nil)
			return
		}
		var change state.RepositoryRefPolicyChange
		if input.KeptHistory != nil {
			kept, valid := state.ParseKeptHistoryChoice(*input.KeptHistory)
			if !valid {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "kept_history must be default, on or off.", nil)
				return
			}
			change.KeptHistory = &kept
		}
		change.ProtectDefaultBranch = input.ProtectDefaultBranch
		saved, warnings, err := app.saveRefPolicy(request.Context(), repositoryID, change)
		if errors.As(err, new(*state.PolicyError)) {
			writeSettingUnreadable(writer, request, "repository settings save", err)
			return
		}
		if err != nil {
			writeAPIError(writer, unavailable(request, "repository settings save", err), "state_unavailable", "The settings could not be saved. Try again later.", nil)
			return
		}
		for _, warning := range warnings {
			response.Warnings = append(response.Warnings, webui.Text(webui.LangEN, warning))
		}
		response.Settings = repositorySettingsJSON{
			KeptHistory: pointer(string(saved.Saved.KeptHistory)), KeptHistoryNow: onOff(saved.Now.KeepHistory),
			ProtectDefaultBranch: pointer(saved.Saved.ProtectDefaultBranch),
		}
		writeAPIJSON(writer, http.StatusOK, response)
		return
	}
	saved, err := app.Store.RepositoryRefPolicy(request.Context(), repositoryID)
	var writes state.RefWrites
	if err == nil {
		writes, err = app.Store.RefWrites(request.Context(), repositoryID)
	}
	if err != nil {
		app.writeSettingsReadError(writer, request, err)
		return
	}
	response.Settings = repositorySettingsJSON{
		KeptHistory: pointer(string(saved.KeptHistory)), KeptHistoryNow: onOff(writes.KeepHistory),
		ProtectDefaultBranch: pointer(saved.ProtectDefaultBranch),
	}
	writeAPIJSON(writer, http.StatusOK, response)
}
