package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// Server-wide policies: how long a sign-in lasts, the branch new
// repositories start on, the Git transfer limits, whether repositories keep
// overwritten and deleted history, and how long raw check logs are kept.
// The Settings tabs and the owner API (/api/v1/settings) read and save them
// through the same state accessors, which hold their choices and bounds.
// The first four apply to what starts after they are saved: a sign-in, a
// new repository, a transfer, a push or an import. The log choice applies
// at once to the logs kept, and the next cleanup deletes the ones it no
// longer keeps.

// tabPolicies reads the policies tab shows to admin, a confirmed
// administrator; nobody else is shown a saved value. A saved value that
// cannot be read (state.PolicyError) does not stop the page: its group says
// so and offers the default to save in its place.
func (app *App) tabPolicies(request *http.Request, tab string, admin bool) (webui.Policies, error) {
	policies := webui.Policies{Unreadable: map[string]bool{}, Visible: admin}
	if !admin {
		return policies, nil
	}
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
		limits, err := app.Store.GitTransferLimits(request.Context())
		if err = unreadable(webui.GroupTransfer, err); err != nil {
			return webui.Policies{}, err
		}
		if limits == (state.GitTransferLimits{}) {
			limits = state.DefaultGitTransferLimits
		}
		policies.TransferSize = webui.FormatLimit(webui.LimitSize, limits.MaximumBytes, "")
		policies.TransferTime = webui.FormatLimit(webui.LimitDuration, limits.Operation.Milliseconds(), "")
		keep, err := app.Store.KeptHistory(request.Context())
		if err = unreadable(webui.GroupHistory, err); err != nil {
			return webui.Policies{}, err
		}
		if policies.Unreadable[webui.GroupHistory] {
			keep = true
		}
		policies.KeptHistory = onOff(keep)
	}
	if tab == webui.SettingsStorage {
		retention, err := app.Store.CheckLogRetention(request.Context())
		if err = unreadable(webui.GroupLogs, err); err != nil {
			return webui.Policies{}, err
		}
		if retention == "" {
			retention = state.DefaultCheckLogRetention
		}
		policies.CheckLogs = string(retention)
	}
	return policies, nil
}

// transferLimitsForm reads the Git transfer limits a Settings form sent,
// or the notices that refuse them.
func transferLimitsForm(request *http.Request) (state.GitTransferLimits, []webui.Notice) {
	var limits state.GitTransferLimits
	var notices []webui.Notice
	size, err := webui.ParseLimit(webui.LimitSize, webui.LimitInput{Amount: postValue(request, "transfer_size"), Unit: postValue(request, "transfer_size_unit")})
	switch {
	case err != nil:
		notices = append(notices, webui.Error("transfer_size", webui.LimitNoticeCode(webui.LimitSize, err)))
	case !state.ValidTransferBytes(size):
		notices = append(notices, webui.Error("transfer_size", webui.MsgCCFieldRange))
	}
	milliseconds, err := webui.ParseLimit(webui.LimitDuration, webui.LimitInput{Amount: postValue(request, "transfer_time"), Unit: postValue(request, "transfer_time_unit")})
	operation := time.Duration(milliseconds) * time.Millisecond
	switch {
	case err != nil:
		notices = append(notices, webui.Error("transfer_time", webui.LimitNoticeCode(webui.LimitDuration, err)))
	case milliseconds > state.MaximumTransferOperation.Milliseconds() || !state.ValidTransferOperation(operation):
		notices = append(notices, webui.Error("transfer_time", webui.MsgCCFieldRange))
	}
	limits.MaximumBytes, limits.Operation = size, operation
	return limits, notices
}

// settingsJSON is the owner API's view of the policies. A PATCH names only
// the ones it changes.
type settingsJSON struct {
	// Session is how long a sign-in with the shared password lasts, one of
	// state.GeneralSessions.
	Session *string `json:"session,omitempty"`
	// InitialBranch is the branch new repositories start on.
	InitialBranch *string `json:"initial_branch,omitempty"`
	// GitTransfer holds the Git transfer limits. A PATCH may name one of
	// them; the other keeps its saved value.
	GitTransfer *gitTransferJSON `json:"git_transfer,omitempty"`
	// CheckLogs is how long raw check logs are kept, one of
	// state.CheckLogRetentions.
	CheckLogs *string `json:"check_logs,omitempty"`
	// KeptHistory is "on" when repositories that follow the server keep
	// overwritten and deleted history, and "off" when they do not.
	KeptHistory *string `json:"kept_history,omitempty"`
}

type gitTransferJSON struct {
	MaximumBytes     *int64 `json:"maximum_bytes,omitempty"`
	OperationSeconds *int64 `json:"operation_seconds,omitempty"`
}

type settingsResponse struct {
	OK       bool         `json:"ok"`
	Settings settingsJSON `json:"settings"`
	// Unreadable names each saved setting that cannot be read, which
	// Settings leaves out. Only a PATCH answers with it: the change it
	// asked for was saved.
	Unreadable []unreadableSetting `json:"unreadable,omitempty"`
	// Warnings say what a PATCH turned off allows now, as Settings does.
	Warnings []string `json:"warnings,omitempty"`
}

type unreadableSetting struct {
	Setting string `json:"setting"`
	Message string `json:"message"`
}

// handleSettingsAPI answers GET and PATCH /api/v1/settings. Both need the
// administrator password in the request, as every administrator API
// request does. A PATCH checks every value it names and saves them all at
// once, or none, and answers with every policy as saved. A GET that cannot
// read a saved setting fails with setting_unreadable; a PATCH that saved
// its change succeeds and names in Unreadable the other settings to set
// again.
func (app *App) handleSettingsAPI(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodPatch {
		writeAPIMethodError(writer, "GET, PATCH")
		return
	}
	if !app.authorizeAdminAPI(writer, request) {
		return
	}
	var warnings []string
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
		if change.GitTransfer != nil {
			limits, problem, err := app.changedTransferLimits(request.Context(), *change.GitTransfer)
			if err != nil {
				app.writeSettingsReadError(writer, request, err)
				return
			}
			if problem != "" {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", problem, nil)
				return
			}
			policies.GitTransfer = &limits
		}
		if change.CheckLogs != nil {
			retention, valid := state.ParseCheckLogRetention(*change.CheckLogs)
			if !valid {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "check_logs must be one of "+choiceList(state.CheckLogRetentions)+".", nil)
				return
			}
			policies.CheckLogs = &retention
		}
		if change.KeptHistory != nil {
			keep, valid := parseOnOff(*change.KeptHistory)
			if !valid {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "kept_history must be on or off.", nil)
				return
			}
			policies.KeptHistory = &keep
			if !keep {
				warnings = append(warnings, webui.Text(webui.LangEN, webui.MsgKeptHistorySavedOff))
			}
		}
		if err := app.Store.SavePolicies(request.Context(), policies); err != nil {
			writeAPIError(writer, unavailable(request, "settings save", err), "state_unavailable", "The settings could not be saved. Try again later.", nil)
			return
		}
	}
	current, unreadable, err := app.savedSettings(request.Context())
	if err == nil && len(unreadable) > 0 && request.Method == http.MethodGet {
		err = unreadable[0]
	}
	if err != nil {
		app.writeSettingsReadError(writer, request, err)
		return
	}
	response := settingsResponse{OK: true, Settings: current, Warnings: warnings}
	for _, problem := range unreadable {
		logFailure(request, "settings read", problem)
		response.Unreadable = append(response.Unreadable, unreadableSetting{Setting: problem.Setting(), Message: problem.Advice()})
	}
	writeAPIJSON(writer, http.StatusOK, response)
}

// savedSettings reads every policy as saved, leaving out and returning in
// unreadable each one whose saved value cannot be read.
func (app *App) savedSettings(ctx context.Context) (current settingsJSON, unreadable []*state.PolicyError, err error) {
	// keep sorts one read: a value, a saved value to set again, or a
	// failure to read the state at all.
	keep := func(readErr error) bool {
		var policyErr *state.PolicyError
		if errors.As(readErr, &policyErr) {
			unreadable = append(unreadable, policyErr)
		} else if readErr != nil && err == nil {
			err = readErr
		}
		return readErr == nil
	}
	if session, readErr := app.Store.GeneralSession(ctx); keep(readErr) {
		current.Session = pointer(string(session))
	}
	if branch, readErr := app.Store.InitialBranch(ctx); keep(readErr) {
		current.InitialBranch = &branch
	}
	if limits, readErr := app.Store.GitTransferLimits(ctx); keep(readErr) {
		current.GitTransfer = &gitTransferJSON{MaximumBytes: &limits.MaximumBytes, OperationSeconds: pointer(int64(limits.Operation / time.Second))}
	}
	if retention, readErr := app.Store.CheckLogRetention(ctx); keep(readErr) {
		current.CheckLogs = pointer(string(retention))
	}
	if kept, readErr := app.Store.KeptHistory(ctx); keep(readErr) {
		current.KeptHistory = pointer(onOff(kept))
	}
	return current, unreadable, err
}

// changedTransferLimits applies a PATCH of the Git transfer limits to the
// saved ones. problem says why the result is refused; err is a saved value
// that cannot be read, which the owner replaces by naming both limits.
func (app *App) changedTransferLimits(ctx context.Context, change gitTransferJSON) (limits state.GitTransferLimits, problem string, err error) {
	if change.MaximumBytes == nil && change.OperationSeconds == nil {
		return limits, "git_transfer must name maximum_bytes, operation_seconds or both.", nil
	}
	if change.MaximumBytes == nil || change.OperationSeconds == nil {
		if limits, err = app.Store.GitTransferLimits(ctx); err != nil {
			return limits, "", err
		}
	}
	if change.MaximumBytes != nil {
		limits.MaximumBytes = *change.MaximumBytes
	}
	if seconds := change.OperationSeconds; seconds != nil {
		if limits.Operation, err = state.TransferOperationSeconds(*seconds); err != nil {
			return limits, fmt.Sprintf("git_transfer: operation_seconds is from %d to %d.", int64(state.MinimumTransferOperation/time.Second), int64(state.MaximumTransferOperation/time.Second)), nil
		}
	}
	if err := limits.Validate(); err != nil {
		return limits, "git_transfer: " + err.Error() + ".", nil
	}
	return limits, "", nil
}

// writeSettingsReadError answers a policy that could not be read: one whose
// saved value the owner has to set again, or a state that could not be
// read now.
func (app *App) writeSettingsReadError(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.As(err, new(*state.PolicyError)) {
		writeSettingUnreadable(writer, request, "settings read", err)
		return
	}
	writeAPIError(writer, unavailable(request, "settings read", err), "state_unavailable", "The settings could not be read. Try again later.", nil)
}

// writeSettingUnreadable answers an operation that a saved setting it needs
// stopped, err holding the state.PolicyError. The log keeps the stored
// value; the answer names the setting and how to set it again.
func writeSettingUnreadable(writer http.ResponseWriter, request *http.Request, operation string, err error) {
	var policyErr *state.PolicyError
	errors.As(err, &policyErr)
	logFailure(request, operation, err)
	writeAPIError(writer, http.StatusConflict, "setting_unreadable", policyErr.Advice(), map[string]string{"setting": policyErr.Setting()})
}

func pointer[T any](value T) *T { return &value }

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

// parseOnOff reads "on" or "off".
func parseOnOff(value string) (on, valid bool) {
	return value == "on", value == "on" || value == "off"
}

// choiceList names the choices of a policy for a refusal.
func choiceList[T ~string](choices []T) string {
	names := make([]string, len(choices))
	for index, choice := range choices {
		names[index] = string(choice)
	}
	return strings.Join(names, ", ")
}
