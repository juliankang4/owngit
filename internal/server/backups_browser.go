package server

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"owngit/internal/backups"
	"owngit/internal/recovery"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// The Backups groups of Settings, Storage & recovery (templates/backups.html):
// the schedule, Back up now, verifying and downloading a listed backup, and
// uploading a backup to restore. Each is an administrator change, behind
// the same CSRF check and administrator confirmation as every Settings
// form, and uses the backups.Service operations the owner API uses.

// backupRefusal is how a refused backup operation is answered, in the
// dashboard and the API alike.
type backupRefusal struct {
	status  int
	code    string
	message webui.MessageCode
	// detail is the cause in English, shown beside the message.
	detail string
}

// backupRefused describes err when it is a refusal the owner can act on,
// and false when it is a failure to answer as unavailable.
func backupRefused(err error) (backupRefusal, bool) {
	switch {
	case errors.Is(err, state.ErrBackupRunning):
		return backupRefusal{http.StatusConflict, "backup_running", webui.MsgBackupRunningRefused, ""}, true
	case errors.Is(err, backups.ErrBusy):
		return backupRefusal{http.StatusConflict, "backup_busy", webui.MsgBackupBusy, ""}, true
	case errors.Is(err, backups.ErrNotConfigured):
		return backupRefusal{http.StatusConflict, "backup_not_configured", webui.MsgBackupChooseFolder, ""}, true
	case errors.Is(err, backups.ErrNoBackup):
		return backupRefusal{http.StatusNotFound, "backup_not_found", webui.MsgBackupNoBackup, ""}, true
	case errors.Is(err, backups.ErrBackupGone):
		return backupRefusal{http.StatusConflict, "backup_gone", webui.MsgBackupGone, err.Error()}, true
	case errors.Is(err, backups.ErrUploadRefused):
		return backupRefusal{http.StatusBadRequest, "invalid_backup_archive", webui.MsgBackupUploadRefused, err.Error()}, true
	}
	return backupRefusal{}, false
}

// apiMessage is the refusal in English, for the API.
func (r backupRefusal) apiMessage() string {
	message := webui.Text(webui.LangEN, r.message)
	if r.detail != "" {
		message += " " + r.detail
	}
	return message
}

// notice is the refusal for the dashboard.
func (r backupRefusal) notice() webui.Notice {
	notice := webui.Error("", r.message)
	notice.Detail = r.detail
	return notice
}

// backupsInfo reads the Backups groups for the Settings page. visible is
// false for a visitor who is not a confirmed administrator, who sees none
// of it.
func (app *App) backupsInfo(request *http.Request, settings state.Settings, visible bool, lang webui.Lang) (webui.BackupsInfo, error) {
	info := webui.BackupsInfo{Visible: visible}
	if !visible || app.Backups == nil {
		return info, nil
	}
	status, err := app.Backups.Status(request.Context())
	if err != nil {
		return info, err
	}
	runs, err := app.Backups.Runs(request.Context())
	if err != nil {
		return info, err
	}
	schedule := status.Schedule
	info.Configured = schedule.State != backups.ScheduleNotConfigured
	info.Scheduled, info.Interval, info.Keep, info.Verify = true, backups.IntervalName(backups.DefaultInterval), backups.DefaultKeep, true
	if info.Configured {
		info.Scheduled, info.Destination, info.Interval, info.Keep, info.Verify = schedule.State == backups.ScheduleOn, schedule.Destination, schedule.Interval, schedule.Keep, *schedule.Verify
	}
	for _, choice := range backups.Intervals {
		info.Intervals = append(info.Intervals, choice.Name)
	}
	info.Running, info.LastRun, info.LastVerified = app.backupRun(status.Running, settings, lang), app.backupRun(status.LastRun, settings, lang), app.backupRun(status.LastVerified, settings, lang)
	if next := status.NextRun; next != nil {
		info.NextRun = *next
		info.NextRunNow = status.Running != nil && !next.After(app.now())
	}
	if limit := status.RestoreLimit; limit != nil {
		info.RestoreLimit = limit.FileSystem
	}
	for index := range runs {
		info.Runs = append(info.Runs, *app.backupRun(&runs[index], settings, lang))
	}
	if check := status.Check; check != nil {
		info.Check = &webui.BackupCheckInfo{Name: check.BackupName, Status: check.Status, Message: check.Message}
	}
	if upload := status.Upload; upload != nil {
		info.Upload = &webui.BackupUploadInfo{Name: upload.Name, Size: upload.Size, Status: upload.Status, Message: upload.Message}
		if upload.RemovesAt != nil {
			info.Upload.RemovesAt = *upload.RemovesAt
		}
		if upload.Status == backups.UploadPassed && upload.Path != "" {
			info.Upload.Restore = app.restoreGuide(upload.Path, settings)
		}
	}
	info.Busy = status.Running != nil || status.Check != nil && status.Check.Status == backups.CheckRunning ||
		status.Upload != nil && status.Upload.Status == backups.UploadVerifying
	return info, nil
}

// backupRun describes a run for the dashboard, with how to restore its
// backup when it has one.
func (app *App) backupRun(run *backups.RunView, settings state.Settings, lang webui.Lang) *webui.BackupRunInfo {
	if run == nil {
		return nil
	}
	info := &webui.BackupRunInfo{
		ID: run.ID, Kind: run.Kind, Status: run.Status, Verification: run.Verification, Name: run.BackupName, Path: run.Path,
		Message: run.Message, StartedAt: run.StartedAt, HoldRepository: run.LongestHoldRepository,
	}
	info.Message = strings.Replace(info.Message, recovery.AliasBranchNotice, webui.Text(lang, webui.MsgBackupAliasBranches), 1)
	if run.FinishedAt != nil {
		info.FinishedAt = *run.FinishedAt
	}
	if run.LongestHoldMS != nil {
		info.HoldKnown, info.HoldMS = true, *run.LongestHoldMS
	}
	if run.Path != "" && run.Status != state.BackupRunning {
		info.Restore = app.restoreGuide(run.Path, settings)
	}
	return info
}

// restoreGuide is how to restore the backup at input on this computer, or
// nil when this server was not told how OwnGit runs here.
func (app *App) restoreGuide(input string, settings state.Settings) *webui.BackupRestore {
	if app.RestoreGuide == nil {
		return nil
	}
	return app.RestoreGuide(input, settings.RepositoryRoot)
}

// backupNotices names the result of each saved backup change, shown in its
// group (settingsNoticeGroups): the schedule's with the warnings of what
// the save turned off.
var backupNotices = map[string][]webui.Notice{
	"backup_saved":                {webui.Success(webui.MsgBackupSaved)},
	"backup_saved_off":            {webui.Success(webui.MsgBackupSaved), {Kind: webui.NoticeWarning, Code: webui.MsgBackupScheduleOffWarning}},
	"backup_saved_unverified":     {webui.Success(webui.MsgBackupSaved), {Kind: webui.NoticeWarning, Code: webui.MsgBackupVerifyOffWarning}},
	"backup_saved_off_unverified": {webui.Success(webui.MsgBackupSaved), {Kind: webui.NoticeWarning, Code: webui.MsgBackupScheduleOffWarning}, {Kind: webui.NoticeWarning, Code: webui.MsgBackupVerifyOffWarning}},
	"backup_started":              {webui.Success(webui.MsgBackupStarted)},
	"backup_check_started":        {webui.Success(webui.MsgBackupCheckStarted)},
	"backup_upload_received":      {webui.Success(webui.MsgBackupUploadReceived)},
}

// backupNoticeGroup is the group that shows a backup result notice, or "".
func backupNoticeGroup(notice string) string {
	switch {
	case backupNotices[notice] == nil:
		return ""
	case strings.HasPrefix(notice, "backup_saved"):
		return webui.GroupBackups
	}
	return webui.GroupBackupRuns
}

// backupAction carries out a backup form of the Settings page, which
// passed the CSRF check and the administrator confirmation.
func (app *App) backupAction(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf, action string) {
	refuse := func(status int, notices ...webui.Notice) {
		app.renderSettingsPage(writer, request, settings, csrf, action, notices, status, settingsView{AdminVerified: true})
	}
	answer := func(err error, step string) {
		if refusal, ok := backupRefused(err); ok {
			refuse(refusal.status, refusal.notice())
			return
		}
		refuse(unavailable(request, step, err), webui.Error("", webui.MsgBackupFailed))
	}
	switch action {
	case webui.ActionSaveBackupSchedule:
		change, notices := backupScheduleForm(request)
		if len(notices) > 0 {
			refuse(http.StatusUnprocessableEntity, notices...)
			return
		}
		before, configured, after, err := app.Backups.ChangeSchedule(request.Context(), change)
		var refused *backups.ChangeError
		if errors.As(err, &refused) {
			notice := webui.Error("", webui.MsgBackupScheduleRefused)
			notice.Detail = refused.Message
			refuse(http.StatusUnprocessableEntity, notice)
			return
		}
		if err != nil {
			answer(err, "backup schedule save")
			return
		}
		notice := "backup_saved"
		if !after.Enabled && (!configured || before.Enabled) {
			notice += "_off"
		}
		if !after.Verify && (!configured || before.Verify) {
			notice += "_unverified"
		}
		app.settingsSaved(writer, request, settingsResultURL(action, notice))
	case webui.ActionBackupNow:
		if _, err := app.Backups.StartNow(); err != nil {
			answer(err, "backup start")
			return
		}
		app.settingsSaved(writer, request, settingsResultURL(action, "backup_started"))
	case webui.ActionBackupVerify:
		if _, err := app.Backups.StartCheck(request.Context(), request.URL.Query().Get("run")); err != nil {
			answer(err, "backup verification start")
			return
		}
		app.settingsSaved(writer, request, settingsResultURL(action, "backup_check_started"))
	case webui.ActionBackupDownload:
		download, err := app.Backups.OpenDownload(request.Context(), request.URL.Query().Get("run"))
		if err != nil {
			answer(err, "backup download")
			return
		}
		app.writeBackupArchive(writer, request, download)
	}
}

// backupScheduleForm reads the schedule form, which sends every part.
func backupScheduleForm(request *http.Request) (backups.ScheduleChange, []webui.Notice) {
	destination := strings.TrimSpace(postValue(request, "backup_destination"))
	enabled, verify := formChecked(postValue(request, "backup_scheduled")), formChecked(postValue(request, "backup_verify"))
	change := backups.ScheduleChange{Destination: &destination, Enabled: &enabled, Verify: &verify}
	var notices []webui.Notice
	if destination == "" {
		notices = append(notices, webui.Error("backup_destination", webui.MsgBackupChooseFolder))
	}
	for _, choice := range backups.Intervals {
		if choice.Name == postValue(request, "backup_interval") {
			interval := choice.Interval
			change.Interval = &interval
		}
	}
	if change.Interval == nil {
		notices = append(notices, webui.Error("backup_interval", webui.MsgSettingsUnknownAct))
	}
	keep, err := strconv.Atoi(strings.TrimSpace(postValue(request, "backup_keep")))
	if err != nil || keep < 1 || keep > state.MaxBackupKeep {
		notices = append(notices, webui.Error("backup_keep", webui.MsgBackupKeepInvalid))
	}
	change.Keep = &keep
	return change, notices
}

// writeBackupArchive sends download as a tar file and closes it. Once the
// file has started, a failure can only cut the connection, so the client
// never takes a shortened archive for a whole one.
func (app *App) writeBackupArchive(writer http.ResponseWriter, request *http.Request, download *backups.Download) {
	defer download.Close()
	request = app.beginTransfer(writer, request)
	header := writer.Header()
	header.Set("Content-Type", "application/x-tar")
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": download.Name + ".tar"}))
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	if err := download.WriteArchive(request.Context(), idleWriter{writer, http.NewResponseController(writer)}); err != nil {
		logFailure(request, "backup download of "+download.Name, err)
		panic(http.ErrAbortHandler)
	}
}

// backupFieldLimit bounds each form field that comes before the file in an
// upload: the CSRF token, the action and the administrator password.
const backupFieldLimit = 16 << 10

// isMultipart reports whether request sends a multipart form, as the
// upload of a backup does.
func isMultipart(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	return err == nil && mediaType == "multipart/form-data"
}

// receiveBackupUpload answers the upload form of the Backups groups. The
// fields before the file are read under the page deadline, and the file
// only once the CSRF check and the administrator confirmation passed.
func (app *App) receiveBackupUpload(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf string) {
	if !requireFormOrigin(request) {
		http.Error(writer, "request origin is required", http.StatusForbidden)
		return
	}
	reader, err := request.MultipartReader()
	if err != nil {
		http.Error(writer, "invalid form", http.StatusBadRequest)
		return
	}
	fields := url.Values{}
	var file *multipart.Part
	for file == nil {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(fields) > 8 {
			http.Error(writer, "invalid form", http.StatusBadRequest)
			return
		}
		if part.FormName() == "backup_file" {
			file = part
			break
		}
		value, err := io.ReadAll(io.LimitReader(part, backupFieldLimit+1))
		if err != nil || len(value) > backupFieldLimit {
			http.Error(writer, "invalid form", http.StatusBadRequest)
			return
		}
		fields.Add(part.FormName(), string(value))
	}
	request.PostForm = fields
	if !app.requireCSRF(writer, request) {
		return
	}
	action := postValue(request, "action")
	if action != webui.ActionBackupUpload {
		app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("action", webui.MsgSettingsUnknownAct)}, http.StatusBadRequest)
		return
	}
	if _, err := app.confirmAdmin(writer, request, nil, false); err != nil {
		notice, status := adminPasswordNotice(request, err, "admin_password")
		app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{notice}, status)
		return
	}
	refuse := func(status int, notice webui.Notice) {
		app.renderSettingsPage(writer, request, settings, csrf, action, []webui.Notice{notice}, status, settingsView{AdminVerified: true})
	}
	switch {
	case file == nil || file.FileName() == "":
		refuse(http.StatusUnprocessableEntity, webui.Error("", webui.MsgBackupUploadMissing))
		return
	case request.ContentLength <= 0:
		refuse(http.StatusLengthRequired, webui.Error("", webui.MsgBackupUploadNoSize))
		return
	}
	// A long upload outlives the page deadline, so the answer uses the
	// transfer's request.
	request = app.beginTransfer(writer, request)
	_, err = app.Backups.ReceiveUpload(request.Context(), idleReader{file, http.NewResponseController(writer)}, request.ContentLength)
	if err != nil {
		if refusal, ok := backupRefused(err); ok {
			refuse(refusal.status, refusal.notice())
			return
		}
		refuse(unavailable(request, "backup upload", err), webui.Error("", webui.MsgBackupFailed))
		return
	}
	app.settingsSaved(writer, request, settingsResultURL(action, "backup_upload_received"))
}
