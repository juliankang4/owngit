package server

import (
	"errors"
	"net/http"
	"strings"

	"owngit/internal/backups"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// backupScheduleJSON is a schedule change: every field is optional, and the
// first change must name a destination.
type backupScheduleJSON struct {
	Destination *string `json:"destination,omitempty"`
	// Scheduled is "on" or "off".
	Scheduled *string `json:"scheduled,omitempty"`
	// Interval is one of backups.Intervals.
	Interval *string `json:"interval,omitempty"`
	Keep     *int    `json:"keep,omitempty"`
	// Verify is "on" or "off".
	Verify *string `json:"verify,omitempty"`
}

// handleBackupsAPI serves, with the administrator password,
// /api/v1/backups (status), /api/v1/backups/runs (list, and POST to back up
// now), /api/v1/backups/runs/ID/verify (POST: verify that backup again),
// /api/v1/backups/runs/ID/archive (GET: download it as a tar file),
// /api/v1/backups/upload (PUT: upload a backup archive to restore) and
// /api/v1/backups/schedule (GET, PATCH). Folders and messages are
// administrator information. /api/v1/backups/summary, which names neither,
// is read with general access too, so that coding agents can check that
// backups work.
func (app *App) handleBackupsAPI(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	switch request.URL.Path {
	case "/api/v1/backups", "/api/v1/backups/summary":
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		summary := request.URL.Path == "/api/v1/backups/summary"
		// The administrator reads the summary too, with the password file
		// every owngit backup command takes.
		username, _, hasBasic := request.BasicAuth()
		if summary && !(hasBasic && username == "admin") {
			if !app.authorizeAPI(writer, request, settings) {
				return
			}
		} else if !app.authorizeAdminAPI(writer, request) {
			return
		}
		status, err := app.Backups.Status(request.Context())
		if err != nil {
			writeAPIError(writer, unavailable(request, "backup status", err), "state_unavailable", "The backup status could not be read. Try again later.", nil)
			return
		}
		if summary {
			writeAPIJSON(writer, http.StatusOK, struct {
				OK bool `json:"ok"`
				backups.Summary
			}{true, backups.Summarize(status)})
			return
		}
		// A verified upload comes with the command that restores it on
		// this computer, as the dashboard shows it.
		var restore *webui.BackupRestore
		if upload := status.Upload; upload != nil && upload.Status == backups.UploadPassed && upload.Path != "" {
			restore = app.restoreGuide(upload.Path, settings)
		}
		var command, problem string
		if restore != nil {
			command = restore.Command
			if restore.Unchecked != "" {
				problem = "OwnGit could not check where " + restore.Unchecked + " will be once the state folder is renamed, so it gives no restore command: " + restore.Problem
			}
		}
		writeAPIJSON(writer, http.StatusOK, struct {
			OK bool `json:"ok"`
			backups.Status
			UploadRestoreCommand string `json:"upload_restore_command,omitempty"`
			UploadRestoreProblem string `json:"upload_restore_problem,omitempty"`
		}{true, status, command, problem})
	case "/api/v1/backups/runs":
		if request.Method != http.MethodGet && request.Method != http.MethodPost {
			writeAPIMethodError(writer, "GET, POST")
			return
		}
		if !app.authorizeAdminAPI(writer, request) {
			return
		}
		if request.Method == http.MethodPost {
			app.startBackup(writer, request)
			return
		}
		runs, err := app.Backups.Runs(request.Context())
		if err != nil {
			writeAPIError(writer, unavailable(request, "backup runs", err), "state_unavailable", "The backups could not be read. Try again later.", nil)
			return
		}
		writeAPIJSON(writer, http.StatusOK, struct {
			OK   bool              `json:"ok"`
			Runs []backups.RunView `json:"runs"`
		}{true, runs})
	case "/api/v1/backups/schedule":
		if request.Method != http.MethodGet && request.Method != http.MethodPatch {
			writeAPIMethodError(writer, "GET, PATCH")
			return
		}
		if !app.authorizeAdminAPI(writer, request) {
			return
		}
		app.backupSchedule(writer, request)
	case "/api/v1/backups/upload":
		if request.Method != http.MethodPut {
			writeAPIMethodError(writer, http.MethodPut)
			return
		}
		if !app.authorizeAdminAPI(writer, request) {
			return
		}
		app.uploadBackup(writer, request)
	default:
		id, operation, found := strings.Cut(strings.TrimPrefix(request.URL.Path, "/api/v1/backups/runs/"), "/")
		if !found || !strings.HasPrefix(request.URL.Path, "/api/v1/backups/runs/") || (operation != "verify" && operation != "archive") {
			writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
			return
		}
		method := map[string]string{"verify": http.MethodPost, "archive": http.MethodGet}[operation]
		if request.Method != method {
			writeAPIMethodError(writer, method)
			return
		}
		if !app.authorizeAdminAPI(writer, request) {
			return
		}
		if operation == "verify" {
			app.checkBackup(writer, request, id)
			return
		}
		download, err := app.Backups.OpenDownload(request.Context(), id)
		if err != nil {
			writeBackupAPIError(writer, request, err, "backup download")
			return
		}
		app.writeBackupArchive(writer, request, download)
	}
}

// writeBackupAPIError answers a backup operation that did not happen.
func writeBackupAPIError(writer http.ResponseWriter, request *http.Request, err error, step string) {
	if refusal, ok := backupRefused(err); ok {
		writeAPIError(writer, refusal.status, refusal.code, refusal.apiMessage(), nil)
		return
	}
	writeAPIError(writer, unavailable(request, step, err), "state_unavailable", "The backup operation could not be carried out. Try again later.", nil)
}

func (app *App) checkBackup(writer http.ResponseWriter, request *http.Request, id string) {
	check, err := app.Backups.StartCheck(request.Context(), id)
	if err != nil {
		writeBackupAPIError(writer, request, err, "backup verification start")
		return
	}
	writeAPIJSON(writer, http.StatusAccepted, struct {
		OK    bool          `json:"ok"`
		Check backups.Check `json:"check"`
	}{true, check})
}

// uploadBackup receives a backup archive, which must declare its size.
func (app *App) uploadBackup(writer http.ResponseWriter, request *http.Request) {
	if request.ContentLength <= 0 {
		writeAPIError(writer, http.StatusLengthRequired, "length_required", webui.Text(webui.LangEN, webui.MsgBackupUploadNoSize), nil)
		return
	}
	request = app.beginTransfer(request)
	upload, err := app.Backups.ReceiveUpload(request.Context(), idleReader{request.Body, http.NewResponseController(writer)}, request.ContentLength)
	if err != nil {
		writeBackupAPIError(writer, request, err, "backup upload")
		return
	}
	writeAPIJSON(writer, http.StatusAccepted, struct {
		OK     bool           `json:"ok"`
		Upload backups.Upload `json:"upload"`
	}{true, upload})
}

func (app *App) startBackup(writer http.ResponseWriter, request *http.Request) {
	run, err := app.Backups.StartNow()
	if err != nil {
		writeBackupAPIError(writer, request, err, "backup start")
		return
	}
	writeAPIJSON(writer, http.StatusAccepted, struct {
		OK  bool             `json:"ok"`
		Run *backups.RunView `json:"run"`
	}{true, backups.ViewRun(run)})
}

func (app *App) backupSchedule(writer http.ResponseWriter, request *http.Request) {
	var warnings []string
	if request.Method == http.MethodPatch {
		var input backupScheduleJSON
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		change, problem := backupChange(input)
		if problem != "" {
			writeAPIError(writer, http.StatusBadRequest, "invalid_backup_schedule", problem, nil)
			return
		}
		before, configured, after, err := app.Backups.ChangeSchedule(request.Context(), change)
		var refused *backups.ChangeError
		switch {
		case errors.As(err, &refused):
			writeAPIError(writer, http.StatusBadRequest, "invalid_backup_schedule", refused.Message, nil)
			return
		case err != nil:
			writeAPIError(writer, unavailable(request, "backup schedule save", err), "state_unavailable", "The backup schedule could not be saved. Try again later.", nil)
			return
		}
		if !after.Enabled && (!configured || before.Enabled) {
			warnings = append(warnings, webui.Text(webui.LangEN, webui.MsgBackupScheduleOffWarning))
		}
		if !after.Verify && (!configured || before.Verify) {
			warnings = append(warnings, webui.Text(webui.LangEN, webui.MsgBackupVerifyOffWarning))
		}
	}
	schedule, configured, err := app.Store.BackupSchedule(request.Context())
	if err != nil {
		writeAPIError(writer, unavailable(request, "backup schedule read", err), "state_unavailable", "The backup schedule could not be read. Try again later.", nil)
		return
	}
	writeAPIJSON(writer, http.StatusOK, struct {
		OK       bool                 `json:"ok"`
		Schedule backups.ScheduleView `json:"schedule"`
		Warnings []string             `json:"warnings,omitempty"`
	}{true, backups.ViewSchedule(schedule, configured), warnings})
}

// backupChange reads a schedule change, or says what is wrong with it.
func backupChange(input backupScheduleJSON) (backups.ScheduleChange, string) {
	var change backups.ScheduleChange
	if input == (backupScheduleJSON{}) {
		return change, "Name at least one part of the schedule to change."
	}
	change.Destination, change.Keep = input.Destination, input.Keep
	if input.Scheduled != nil {
		on, valid := parseOnOff(*input.Scheduled)
		if !valid {
			return change, "scheduled must be on or off."
		}
		change.Enabled = &on
	}
	if input.Verify != nil {
		on, valid := parseOnOff(*input.Verify)
		if !valid {
			return change, "verify must be on or off."
		}
		change.Verify = &on
	}
	if input.Interval != nil {
		var names []string
		for _, choice := range backups.Intervals {
			names = append(names, choice.Name)
			if choice.Name == *input.Interval {
				interval := choice.Interval
				change.Interval = &interval
			}
		}
		if change.Interval == nil {
			return change, "interval must be one of " + strings.Join(names, ", ") + "."
		}
	}
	return change, ""
}
