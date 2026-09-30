package server

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// Server-wide policies: how long a sign-in lasts, the branch new
// repositories start on, the Git transfer limits, whether repositories keep
// overwritten and deleted history, how long raw check logs are kept, and
// the access policies (access_policies.go).
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
		links, err := app.Store.CrossSiteLinks(request.Context())
		if err = unreadable(webui.GroupCrossSite, err); err != nil {
			return webui.Policies{}, err
		}
		if links == "" {
			links = state.DefaultCrossSiteLinks
		}
		policies.CrossSiteLinks = string(links)
		limits, err := app.Store.LoginLimits(request.Context())
		if err = unreadable(webui.GroupLogin, err); err != nil {
			return webui.Policies{}, err
		}
		if limits == (state.LoginLimits{}) {
			limits = state.DefaultLoginLimits
		}
		policies.LoginAttempts = strconv.Itoa(limits.Attempts)
		policies.LoginWindow = webui.FormatLimit(webui.LimitDuration, limits.Window.Milliseconds(), "")
		policies.LoginPause = webui.FormatLimit(webui.LimitDuration, limits.Pause.Milliseconds(), "")
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
		policies.TransferPerRepository = strconv.Itoa(limits.PerRepository)
		policies.TransferExtraSlots = strconv.Itoa(limits.ExtraSlots)
		policies.TransferIdle = webui.FormatLimit(webui.LimitDuration, limits.Idle.Milliseconds(), "")
		policies.TransferQueue = webui.FormatLimit(webui.LimitDuration, limits.QueueWait.Milliseconds(), "")
		keep, err := app.Store.KeptHistory(request.Context())
		if err = unreadable(webui.GroupHistory, err); err != nil {
			return webui.Policies{}, err
		}
		if policies.Unreadable[webui.GroupHistory] {
			keep = true
		}
		policies.KeptHistory = onOff(keep)
		ask, err := app.Store.DeleteRequiresName(request.Context())
		if err = unreadable(webui.GroupDeleteName, err); err != nil {
			return webui.Policies{}, err
		}
		if policies.Unreadable[webui.GroupDeleteName] {
			ask = true
		}
		policies.DeleteRequiresName = onOff(ask)
		browse, err := app.Store.BrowseLimits(request.Context())
		if err = unreadable(webui.GroupBrowse, err); err != nil {
			return webui.Policies{}, err
		}
		if policies.Unreadable[webui.GroupBrowse] {
			browse = state.DefaultBrowseLimits
		}
		size := func(bytes int64) webui.LimitInput { return webui.FormatLimit(webui.LimitSize, bytes, "") }
		policies.Browse = webui.BrowsePolicies{
			Raw: size(browse.RawBytes), File: size(browse.FileBytes), CommitPatch: size(browse.CommitPatchBytes),
			FilePatch: size(browse.FilePatchBytes), CommitFile: size(browse.CommitFileBytes), Compare: size(browse.CompareBytes),
			CompareTime: webui.FormatLimit(webui.LimitDuration, browse.CompareTime.Milliseconds(), ""),
		}
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
		choices, err := app.Store.Maintenance(request.Context())
		if err = unreadable(webui.GroupMaintenance, err); err != nil {
			return webui.Policies{}, err
		}
		if policies.Unreadable[webui.GroupMaintenance] {
			choices = state.DefaultMaintenance
		}
		duration := func(d time.Duration) webui.LimitInput {
			return webui.FormatLimit(webui.LimitDuration, d.Milliseconds(), "")
		}
		policies.Maintenance = webui.MaintenancePolicies{
			Enabled: onOff(choices.Enabled), WindowStart: strconv.Itoa(choices.WindowStart), WindowEnd: strconv.Itoa(choices.WindowEnd),
			Idle: duration(choices.Idle), Command: duration(choices.CommandTime), FullRepack: duration(choices.FullRepackTime),
			PackThreshold: strconv.Itoa(choices.PackThreshold),
		}
		cleanup, err := app.Store.UnusedObjectCleanup(request.Context())
		if err = unreadable(webui.GroupCleanup, err); err != nil {
			return webui.Policies{}, err
		}
		if policies.Unreadable[webui.GroupCleanup] {
			cleanup = state.DefaultUnusedObjectCleanup
		}
		policies.Cleanup = webui.CleanupPolicies{Enabled: onOff(cleanup.Enabled), GraceDays: strconv.FormatInt(int64(cleanup.Grace/(24*time.Hour)), 10)}
	}
	return policies, nil
}

// transferLimitsForm reads the Git transfer limits a Settings form sent,
// or the notices that refuse them.
func transferLimitsForm(request *http.Request) (state.GitTransferLimits, []webui.Notice) {
	form := &limitsForm{request: request}
	limits := state.GitTransferLimits{
		MaximumBytes:  form.size("transfer_size", state.MinimumTransferBytes, state.MaximumTransferBytes),
		Operation:     form.duration("transfer_time", state.MinimumTransferOperation, state.MaximumTransferOperation),
		PerRepository: form.count("transfer_per_repository", 1, state.MaximumTransfersAtOnce),
		ExtraSlots:    form.count("transfer_extra_slots", 0, state.MaximumTransfersAtOnce),
		Idle:          form.duration("transfer_idle", state.MinimumTransferIdle, state.MaximumTransferIdle),
		QueueWait:     form.duration("transfer_queue", state.MinimumTransferQueue, state.MaximumTransferQueue),
	}
	return limits, form.notices
}

// browseLimitsForm reads the browsing limits a Settings form sent, or the
// notices that refuse them.
func browseLimitsForm(request *http.Request) (state.BrowseLimits, []webui.Notice) {
	form := &limitsForm{request: request}
	view := func(field string) int64 { return form.size(field, state.MinimumBrowseBytes, state.MaximumBrowseBytes) }
	limits := state.BrowseLimits{
		RawBytes:  form.size("browse_raw", state.MinimumRawBytes, state.MaximumRawBytes),
		FileBytes: view("browse_file"), CommitPatchBytes: view("browse_commit_patch"), FilePatchBytes: view("browse_file_patch"),
		CommitFileBytes: form.size("browse_commit_file", state.MinimumCommitFileBytes, state.MaximumCommitFileBytes),
		CompareBytes:    view("browse_compare"),
		CompareTime:     form.duration("browse_compare_time", state.MinimumCompareTime, state.MaximumCompareTime),
	}
	return limits, form.notices
}

// maintenanceForm reads the maintenance choices a Settings form sent, or
// the notices that refuse them.
func maintenanceForm(request *http.Request) (state.Maintenance, []webui.Notice) {
	form := &limitsForm{request: request}
	enabled, valid := parseOnOff(postValue(request, "maintenance_enabled"))
	if !valid {
		form.notices = append(form.notices, webui.Error("maintenance_enabled", webui.MsgSettingsUnknownAct))
	}
	choices := state.Maintenance{
		Enabled:        enabled,
		WindowStart:    form.count("maintenance_window_start", 0, 23),
		WindowEnd:      form.count("maintenance_window_end", 0, 23),
		Idle:           form.duration("maintenance_idle", state.MinimumMaintenanceTime, state.MaximumMaintenanceTime),
		CommandTime:    form.duration("maintenance_command", state.MinimumMaintenanceTime, state.MaximumMaintenanceTime),
		FullRepackTime: form.duration("maintenance_full_repack", state.MinimumMaintenanceTime, state.MaximumMaintenanceTime),
		PackThreshold:  form.count("maintenance_pack_threshold", state.MinimumPackThreshold, state.MaximumPackThreshold),
	}
	if len(form.notices) == 0 && choices.WindowStart == choices.WindowEnd {
		form.notices = append(form.notices, webui.Error("maintenance_window_end", webui.MsgMaintenanceWindowSame))
	}
	return choices, form.notices
}

// cleanupForm reads the unused object cleanup choice a Settings form sent,
// or the notices that refuse it.
func cleanupForm(request *http.Request) (state.UnusedObjectCleanup, []webui.Notice) {
	form := &limitsForm{request: request}
	enabled, valid := parseOnOff(postValue(request, "cleanup_enabled"))
	if !valid {
		form.notices = append(form.notices, webui.Error("cleanup_enabled", webui.MsgSettingsUnknownAct))
	}
	days := form.count("cleanup_grace", int(state.MinimumCleanupGrace/(24*time.Hour)), int(state.MaximumCleanupGrace/(24*time.Hour)))
	return state.UnusedObjectCleanup{Enabled: enabled, Grace: time.Duration(days) * 24 * time.Hour}, form.notices
}

// limitsForm reads the numeric fields of a Settings form, each checked
// against its bounds, and collects a notice for each field that is
// refused.
type limitsForm struct {
	request *http.Request
	notices []webui.Notice
}

// count reads a whole number from minimum to maximum.
func (form *limitsForm) count(field string, minimum, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(postValue(form.request, field)))
	if err != nil || value < minimum || value > maximum {
		form.notices = append(form.notices, webui.Error(field, webui.MsgCCFieldRange))
	}
	return value
}

// size reads an amount with its unit (field_unit) in bytes, from minimum
// to maximum.
func (form *limitsForm) size(field string, minimum, maximum int64) int64 {
	return form.limit(webui.LimitSize, field, minimum, maximum, 1)
}

// duration reads an amount with its unit (field_unit), a whole number of
// seconds from minimum to maximum.
func (form *limitsForm) duration(field string, minimum, maximum time.Duration) time.Duration {
	milliseconds := form.limit(webui.LimitDuration, field, minimum.Milliseconds(), maximum.Milliseconds(), 1000)
	return time.Duration(milliseconds) * time.Millisecond
}

// limit reads an amount of kind in its stored unit, a multiple of step
// from minimum to maximum.
func (form *limitsForm) limit(kind webui.LimitKind, field string, minimum, maximum, step int64) int64 {
	value, err := webui.ParseLimit(kind, webui.LimitInput{Amount: postValue(form.request, field), Unit: postValue(form.request, field+"_unit")})
	switch {
	case err != nil:
		form.notices = append(form.notices, webui.Error(field, webui.LimitNoticeCode(kind, err)))
	case value < minimum || value > maximum || value%step != 0:
		form.notices = append(form.notices, webui.Error(field, webui.MsgCCFieldRange))
	}
	return value
}

// settingsJSON is the owner API's view of the policies. A PATCH names only
// the ones it changes.
type settingsJSON struct {
	// Session is how long a sign-in with the shared password lasts, one of
	// state.GeneralSessions.
	Session *string `json:"session,omitempty"`
	// InitialBranch is the branch new repositories start on.
	InitialBranch *string `json:"initial_branch,omitempty"`
	// GitTransfer holds the Git transfer limits. A PATCH may name some of
	// them; the others keep their saved values.
	GitTransfer *state.GitTransferFields `json:"git_transfer,omitempty"`
	// CheckLogs is how long raw check logs are kept, one of
	// state.CheckLogRetentions.
	CheckLogs *string `json:"check_logs,omitempty"`
	// KeptHistory is "on" when repositories that follow the server keep
	// overwritten and deleted history, and "off" when they do not.
	KeptHistory *string `json:"kept_history,omitempty"`
	// DeleteRequiresName is "on" when deleting a repository asks for its
	// typed name, and "off" when it does not.
	DeleteRequiresName *string `json:"delete_requires_name,omitempty"`
	// LoginLimits are the login attempt limits. A PATCH may name some of
	// them; the others keep their saved values.
	LoginLimits *loginLimitsJSON `json:"login_limits,omitempty"`
	// CrossSiteLinks is "strict" or "lax": whether a link from another site
	// keeps the shared sign-in.
	CrossSiteLinks *string `json:"cross_site_links,omitempty"`
	// BrowseLimits, Maintenance and UnusedObjectCleanup are the browsing
	// limits, repository maintenance and unused object cleanup. A PATCH may
	// name some fields of each; the others keep their saved values.
	BrowseLimits        *state.BrowseFields      `json:"browse_limits,omitempty"`
	Maintenance         *state.MaintenanceFields `json:"maintenance,omitempty"`
	UnusedObjectCleanup *state.CleanupFields     `json:"unused_object_cleanup,omitempty"`
	// UpdateCheck is "on" when the daily new-release check may run, and
	// "off" when it may not.
	UpdateCheck *string `json:"update_check,omitempty"`
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
	// UpdateCheckForcedOff is true when the server started with
	// --no-update-check, which overrides update_check.
	UpdateCheckForcedOff bool `json:"update_check_forced_off,omitempty"`
	// AccessMode is "open" or "password", and AdminConfirmation how long a
	// browser remembers the administrator password. Each is changed at its
	// own address (handleOwnerSettingAPI).
	AccessMode        string `json:"access_mode"`
	AdminConfirmation string `json:"admin_confirmation"`
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
			limits, problem, err := changedGroup(request.Context(), "git_transfer", *change.GitTransfer, app.Store.GitTransferLimits)
			if err != nil {
				app.writeSettingsReadError(writer, request, err)
				return
			}
			if problem != "" {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", problem, nil)
				return
			}
			policies.GitTransfer = &limits
			if limits.Looser() {
				warnings = append(warnings, webui.Text(webui.LangEN, webui.MsgTransferSavedLooser))
			}
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
		if change.DeleteRequiresName != nil {
			ask, valid := parseOnOff(*change.DeleteRequiresName)
			if !valid {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "delete_requires_name must be on or off.", nil)
				return
			}
			policies.DeleteRequiresName = &ask
			if !ask {
				warnings = append(warnings, webui.Text(webui.LangEN, webui.MsgDeleteNameSavedOff))
			}
		}
		if change.LoginLimits != nil {
			limits, problem, err := app.changedLoginLimits(request.Context(), *change.LoginLimits)
			if err != nil {
				app.writeSettingsReadError(writer, request, err)
				return
			}
			if problem != "" {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", problem, nil)
				return
			}
			policies.LoginLimits = &limits
			if limits.Looser() {
				warnings = append(warnings, webui.Text(webui.LangEN, webui.MsgLoginLimitsSavedLooser))
			}
		}
		if change.CrossSiteLinks != nil {
			links, valid := state.ParseCrossSiteLinks(*change.CrossSiteLinks)
			if !valid {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "cross_site_links must be one of "+choiceList(state.CrossSiteChoices)+".", nil)
				return
			}
			policies.CrossSiteLinks = &links
			if links == state.CrossSiteLax {
				warnings = append(warnings, webui.Text(webui.LangEN, webui.MsgCrossSiteSavedLax))
			}
		}
		if change.BrowseLimits != nil {
			limits, problem, err := changedGroup(request.Context(), "browse_limits", *change.BrowseLimits, app.Store.BrowseLimits)
			if err != nil {
				app.writeSettingsReadError(writer, request, err)
				return
			}
			if problem != "" {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", problem, nil)
				return
			}
			policies.Browse = &limits
			if limits.Looser() {
				warnings = append(warnings, webui.Text(webui.LangEN, webui.MsgBrowseWarning))
			}
		}
		if change.Maintenance != nil {
			choices, problem, err := changedGroup(request.Context(), "maintenance", *change.Maintenance, app.Store.Maintenance)
			if err != nil {
				app.writeSettingsReadError(writer, request, err)
				return
			}
			if problem != "" {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", problem, nil)
				return
			}
			policies.Maintenance = &choices
			if choices.Looser() {
				warnings = append(warnings, webui.Text(webui.LangEN, webui.MsgMaintenanceWarning))
			}
		}
		if change.UnusedObjectCleanup != nil {
			cleanup, problem, err := changedGroup(request.Context(), "unused_object_cleanup", *change.UnusedObjectCleanup, app.Store.UnusedObjectCleanup)
			if err != nil {
				app.writeSettingsReadError(writer, request, err)
				return
			}
			if problem != "" {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", problem, nil)
				return
			}
			policies.Cleanup = &cleanup
			if cleanup.Enabled {
				warnings = append(warnings, webui.Text(webui.LangEN, webui.MsgCleanupWarning))
			}
		}
		if change.UpdateCheck != nil {
			check, valid := parseOnOff(*change.UpdateCheck)
			if !valid {
				writeAPIError(writer, http.StatusBadRequest, "invalid_settings", "update_check must be on or off.", nil)
				return
			}
			policies.UpdateCheck = &check
		}
		if err := app.savePolicies(request.Context(), policies); err != nil {
			writeAPIError(writer, unavailable(request, "settings save", err), "state_unavailable", "The settings could not be saved. Try again later.", nil)
			return
		}
		if policies.Maintenance != nil || policies.Cleanup != nil {
			app.Repositories.WakeMaintenance()
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
	response := settingsResponse{OK: true, Settings: current, Warnings: warnings, UpdateCheckForcedOff: app.Releases == nil}
	settings, err := app.Store.Settings(request.Context())
	var confirmation state.AdminConfirmation
	if err == nil {
		confirmation, _, err = app.Store.AdminConfirmation(request.Context())
	}
	if err != nil {
		app.writeSettingsReadError(writer, request, err)
		return
	}
	response.Settings.UpdateCheck = pointer(onOff(settings.UpdateCheck))
	response.AccessMode, response.AdminConfirmation = settings.AccessMode, string(confirmation)
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
		current.GitTransfer = pointer(limits.Fields())
	}
	if retention, readErr := app.Store.CheckLogRetention(ctx); keep(readErr) {
		current.CheckLogs = pointer(string(retention))
	}
	if kept, readErr := app.Store.KeptHistory(ctx); keep(readErr) {
		current.KeptHistory = pointer(onOff(kept))
	}
	if ask, readErr := app.Store.DeleteRequiresName(ctx); keep(readErr) {
		current.DeleteRequiresName = pointer(onOff(ask))
	}
	if limits, readErr := app.Store.LoginLimits(ctx); keep(readErr) {
		current.LoginLimits = loginLimitsAPI(limits)
	}
	if links, readErr := app.Store.CrossSiteLinks(ctx); keep(readErr) {
		current.CrossSiteLinks = pointer(string(links))
	}
	if limits, readErr := app.Store.BrowseLimits(ctx); keep(readErr) {
		current.BrowseLimits = pointer(limits.Fields())
	}
	if choices, readErr := app.Store.Maintenance(ctx); keep(readErr) {
		current.Maintenance = pointer(choices.Fields())
	}
	if cleanup, readErr := app.Store.UnusedObjectCleanup(ctx); keep(readErr) {
		current.UnusedObjectCleanup = pointer(cleanup.Fields())
	}
	return current, unreadable, err
}

// changedGroup applies change, a PATCH of the group named name in its
// JSON form, to the value read saved. problem says why the result is
// refused; err is a saved value that cannot be read, which a change that
// names every field of the group replaces.
func changedGroup[T any, F interface{ Apply(T) (T, error) }](ctx context.Context, name string, change F, read func(context.Context) (T, error)) (value T, problem string, err error) {
	named, total := namedFields(change)
	if named == 0 {
		return value, name + " must name at least one of its fields.", nil
	}
	saved, err := read(ctx)
	if errors.As(err, new(*state.PolicyError)) && named == total {
		saved, err = value, nil
	}
	if err != nil {
		return value, "", err
	}
	if value, err = change.Apply(saved); err != nil {
		return value, name + ": " + err.Error() + ".", nil
	}
	return value, "", nil
}

// namedFields counts the fields a group's JSON form names: those that are
// not nil, of all its fields.
func namedFields(fields any) (named, total int) {
	value := reflect.ValueOf(fields)
	for index := range value.NumField() {
		if !value.Field(index).IsNil() {
			named++
		}
	}
	return named, value.NumField()
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
