package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// Server-wide policies: how long a sign-in lasts and the branch new
// repositories start on. The Settings tabs and the owner API
// (/api/v1/settings) read and save them through the same state accessors,
// which hold their choices and bounds. Each applies from the moment it is
// saved to what starts afterwards: a sign-in, a new repository.

// tabPolicies reads the policies tab shows. A saved value that cannot be
// read (state.PolicyError) does not stop the page: its group says so and
// offers the default to save in its place.
func (app *App) tabPolicies(request *http.Request, tab string) (webui.Policies, error) {
	policies := webui.Policies{Unreadable: map[string]bool{}}
	// unreadable reports a saved value the owner has to set again, and
	// passes on any other error.
	unreadable := func(group string, err error) error {
		var policyErr *state.PolicyError
		if errors.As(err, &policyErr) {
			logFailure(request, "settings read", err)
			policies.Unreadable[group] = true
			return nil
		}
		return err
	}
	if tab == webui.SettingsAccess {
		session, err := app.Store.GeneralSession(request.Context())
		if err = unreadable(webui.GroupSession, err); err != nil {
			return webui.Policies{}, err
		}
		if session == "" {
			session = state.DefaultGeneralSession
		}
		policies.Session = string(session)
	}
	if tab == webui.SettingsRepositories {
		branch, err := app.Store.InitialBranch(request.Context())
		if err = unreadable(webui.GroupBranch, err); err != nil {
			return webui.Policies{}, err
		}
		if branch == "" {
			branch = state.DefaultInitialBranch
		}
		policies.InitialBranch = branch
	}
	return policies, nil
}

// settingsJSON is the owner API's view of the policies. A PATCH names only
// the ones it changes.
type settingsJSON struct {
	// Session is how long a sign-in with the shared password lasts, one of
	// state.GeneralSessions.
	Session *string `json:"session,omitempty"`
	// InitialBranch is the branch new repositories start on.
	InitialBranch *string `json:"initial_branch,omitempty"`
}

type settingsResponse struct {
	OK       bool         `json:"ok"`
	Settings settingsJSON `json:"settings"`
}

// handleSettingsAPI answers GET and PATCH /api/v1/settings. Both need the
// administrator password in the request, as every administrator API
// request does. A PATCH checks every value it names and saves them all at
// once, or none, and answers with every policy as saved.
func (app *App) handleSettingsAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodPatch {
		writeAPIMethodError(writer, "GET, PATCH")
		return
	}
	if !app.authorizeAdminAPI(writer, request) {
		return
	}
	if request.Method == http.MethodPatch {
		var change settingsJSON
		if !decodeAPIJSON(writer, request, &change) {
			return
		}
		if change == (settingsJSON{}) {
			writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "Name at least one setting to change.", nil)
			return
		}
		var policies state.PolicyChange
		if change.Session != nil {
			session, valid := state.ParseGeneralSession(*change.Session)
			if !valid {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "session must be one of "+choiceList(state.GeneralSessions)+".", nil)
				return
			}
			policies.Session = &session
		}
		if change.InitialBranch != nil {
			if err := state.ValidateInitialBranch(*change.InitialBranch); err != nil {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "initial_branch: "+err.Error()+".", nil)
				return
			}
			policies.InitialBranch = change.InitialBranch
		}
		if err := app.Store.SavePolicies(request.Context(), policies); err != nil {
			writeAPIError(writer, unavailable(request, "settings save", err), "state_unavailable", "The settings could not be saved. Try again later.", nil)
			return
		}
	}
	current, err := app.savedSettings(request.Context())
	if err != nil {
		app.writeSettingsReadError(writer, request, err)
		return
	}
	writeAPIJSON(writer, http.StatusOK, settingsResponse{OK: true, Settings: current})
}

// savedSettings reads every policy as saved.
func (app *App) savedSettings(ctx context.Context) (settingsJSON, error) {
	session, err := app.Store.GeneralSession(ctx)
	if err != nil {
		return settingsJSON{}, err
	}
	branch, err := app.Store.InitialBranch(ctx)
	if err != nil {
		return settingsJSON{}, err
	}
	return settingsJSON{Session: pointer(string(session)), InitialBranch: &branch}, nil
}

// writeSettingsReadError answers a policy that could not be read: one whose
// saved value the owner has to set again, or a state that could not be
// read now.
func (app *App) writeSettingsReadError(writer http.ResponseWriter, request *http.Request, err error) {
	var policyErr *state.PolicyError
	if errors.As(err, &policyErr) {
		logFailure(request, "settings read", err)
		writeAPIError(writer, http.StatusConflict, "setting_unreadable", policyErr.Error(), nil)
		return
	}
	writeAPIError(writer, unavailable(request, "settings read", err), "state_unavailable", "The settings could not be read. Try again later.", nil)
}

func pointer[T any](value T) *T { return &value }

// choiceList names the choices of a policy for a refusal.
func choiceList[T ~string](choices []T) string {
	names := make([]string, len(choices))
	for index, choice := range choices {
		names[index] = string(choice)
	}
	return strings.Join(names, ", ")
}
