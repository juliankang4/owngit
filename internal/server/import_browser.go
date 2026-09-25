package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/importsync"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func (app *App) handleNewImport(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	session, ok := app.requireBrowserAdmin(writer, request)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionOverview, "", session.CSRF)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	page := webui.NewImportPage{Chrome: chrome, SubmitURL: "/repositories/new-import"}
	if request.Method == http.MethodGet {
		app.render(writer, http.StatusOK, page)
		return
	}
	if !parseForm(writer, request) {
		return
	}
	page.Name = strings.TrimSpace(postValue(request, "name"))
	page.Description = strings.TrimSpace(postValue(request, "description"))
	page.URL = strings.TrimSpace(postValue(request, "url"))
	page.Mode = postValue(request, "mode")
	page.GitOnlyConsent = postValue(request, "git_only_consent") == "1"
	page.PrivateNetwork = postValue(request, "allow_private_network") == "1"
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	if ok, status := app.importAdminPassword(writer, request, &chrome); !ok {
		page.Chrome = chrome
		app.render(writer, status, page)
		return
	}
	if app.Imports == nil {
		chrome.Notices = []webui.Notice{webui.Error("", webui.MsgImportUnavailable)}
		page.Chrome = chrome
		app.render(writer, http.StatusServiceUnavailable, page)
		return
	}
	credential, credErr := postedImportCredential(request)
	if credErr != nil {
		chrome.Notices = []webui.Notice{importFailureNotice(credErr, "")}
		page.Chrome = chrome
		app.render(writer, http.StatusUnprocessableEntity, page)
		return
	}
	result, err := app.Imports.Import(request.Context(), importsync.ImportInput{
		Name: page.Name, Description: page.Description, URL: page.URL, Mode: importsync.Mode(page.Mode),
		GitOnlyConsent: page.GitOnlyConsent, AllowPrivateNetwork: page.PrivateNetwork, Credentials: credential, Limits: app.importRunLimits(),
	})
	notice := "import_started"
	if err != nil {
		cancelled := importsyncProblemCode(err) == importsync.CodeCancelled
		_, repositoryExists, lookupErr := app.Store.Repository(request.Context(), result.RepositoryID)
		if !cancelled || result.RepositoryID == "" || lookupErr != nil || !repositoryExists {
			// Nothing to show on a repository page: keep the form and explain.
			if cancelled {
				chrome.Notices = []webui.Notice{{Kind: webui.NoticeWarning, Code: webui.MsgImportCancelledNoRepo}}
			} else if errors.Is(err, repository.ErrReservedName) {
				chrome.Notices = []webui.Notice{webui.Error("", webui.MsgRepoNameReserved)}
			} else {
				chrome.Notices = []webui.Notice{importFailureNotice(err, result.Run.ErrorClass)}
			}
			page.Chrome = chrome
			app.render(writer, importProblemStatus(err), page)
			return
		}
		notice = "import_run_cancelled"
	}
	http.Redirect(writer, request, "/repositories/"+url.PathEscape(result.RepositoryID)+"/import?notice="+notice, http.StatusSeeOther)
}

func (app *App) handleImportPage(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	writer.Header().Set("Cache-Control", "no-store")
	session, ok := app.requireBrowserAdmin(writer, request)
	if !ok {
		return
	}
	var err error
	chrome, err = app.chrome(writer, request, webui.SectionRepository, stored.ID, session.CSRF)
	if err != nil {
		app.writePlainError(writer, http.StatusServiceUnavailable)
		return
	}
	if request.Method == http.MethodGet {
		app.renderImportPage(writer, request, stored, summary, chrome, http.StatusOK)
		return
	}
	if !parseForm(writer, request) {
		return
	}
	if !app.validCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	if ok, status := app.importAdminPassword(writer, request, &chrome); !ok {
		app.renderImportPage(writer, request, stored, summary, chrome, status)
		return
	}
	if app.Imports == nil {
		chrome.Notices = append(chrome.Notices, webui.Error("", webui.MsgImportUnavailable))
		app.renderImportPage(writer, request, stored, summary, chrome, http.StatusServiceUnavailable)
		return
	}
	notice := "import_saved"
	status := http.StatusSeeOther
	refresh := false
	switch postValue(request, "action") {
	case webui.ActionImportConfigure:
		_, err = app.Imports.ConfigureSource(request.Context(), importsync.ConfigureInput{
			RepositoryID: stored.ID, URL: postValue(request, "url"), Mode: importsync.Mode(postValue(request, "mode")),
			GitOnlyConsent: postValue(request, "git_only_consent") == "1", AllowPrivateNetwork: postValue(request, "allow_private_network") == "1",
		})
		notice = "import_saved"
	case webui.ActionImportCredentials:
		var credential *importsync.Credentials
		credential, err = postedImportCredential(request)
		if err == nil && credential == nil {
			// Save never clears. Only the explicit Clear credentials action
			// removes the stored credential and CA.
			chrome.Notices = append(chrome.Notices, webui.Error("", webui.MsgImportNothingToSave))
			app.renderImportPage(writer, request, stored, summary, chrome, http.StatusUnprocessableEntity)
			return
		}
		if err == nil {
			err = app.Imports.SetCredentials(request.Context(), stored.ID, credential)
		}
		notice = "import_credentials_saved"
	case webui.ActionImportClearCredentials:
		err = app.Imports.SetCredentials(request.Context(), stored.ID, nil)
		notice = "import_credentials_cleared"
	case webui.ActionImportRefresh:
		_, err = app.Imports.Refresh(request.Context(), stored.ID, app.importRunLimits())
		refresh = true
		notice = "import_refreshed"
		if importsyncProblemCode(err) == importsync.CodeCancelled {
			err = nil
			notice = "import_run_cancelled"
		}
	case webui.ActionImportCancel:
		var cancelled bool
		cancelled, err = app.Imports.Cancel(request.Context(), stored.ID)
		notice = "import_cancelled"
		if err == nil && !cancelled {
			notice = "import_cancel_none"
		}
	case webui.ActionImportResolve:
		_, err = app.Imports.ResolveUnresolved(request.Context(), stored.ID)
		notice = "import_resolved"
	case webui.ActionImportSchedule:
		interval, parseErr := time.ParseDuration(postValue(request, "interval"))
		if parseErr != nil {
			err = &importsync.Problem{Code: importsync.CodeInvalidSchedule, Message: "schedule interval is not a duration", Cause: parseErr}
		} else {
			_, err = app.Imports.SetSchedule(request.Context(), stored.ID, postValue(request, "enabled") == "1", interval)
		}
		notice = "import_schedule_saved"
	default:
		app.renderError(writer, request, http.StatusBadRequest, webui.MsgErrNotFound, "")
		return
	}
	if err != nil {
		if refresh {
			// The Last run section explains the recorded failure class, so
			// the notice stays generic instead of repeating it.
			chrome.Notices = append(chrome.Notices, webui.Error("", webui.MsgImportFailed))
		} else {
			chrome.Notices = append(chrome.Notices, importFailureNotice(err, ""))
		}
		app.renderImportPage(writer, request, stored, summary, chrome, importProblemStatus(err))
		return
	}
	http.Redirect(writer, request, "/repositories/"+url.PathEscape(stored.ID)+"/import?notice="+notice, status)
}

func (app *App) renderImportPage(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, status int) {
	base := app.baseRepositoryPage(request, chrome, stored, summary)
	page := webui.ImportPage{
		Chrome: chrome, Repo: base.Repo, SubmitURL: base.Repo.URL + "/import", SelfURL: base.Repo.URL + "/import",
		Tabs: repositoryTabs(base, webui.RepoTabImport),
	}
	if app.Imports == nil {
		app.render(writer, status, page)
		return
	}
	page.Available = true
	importStatus, err := app.Imports.Status(request.Context(), stored.ID)
	if err != nil {
		page.Available = false
		app.render(writer, http.StatusServiceUnavailable, page)
		return
	}
	page.Configured = importStatus.Configured
	page.URL = importStatus.URL
	page.Mode = importStatus.Mode
	page.GitOnlyConsent = importStatus.GitOnlyConsent
	page.PrivateNetwork = importStatus.TransportConsent
	page.CredentialForm = importStatus.CredentialForm
	page.CredentialBound = importStatus.CredentialBound
	page.CAPresent = importStatus.CAPresent
	page.ContentIncomplete = importStatus.Content.Incomplete
	page.Unresolved = importStatus.UnresolvedIntents
	page.StagingIssues = importStatus.StagingIssues
	page.SchedulerFailed = importStatus.Runtime.SchedulerFailed
	page.ReconcileFailed = !importStatus.Runtime.SchedulerFailed && importStatus.Runtime.Code != ""
	page.RefsTruncated = importStatus.RefsTruncated
	if importStatus.Schedule != nil {
		page.ScheduleEnabled = importStatus.Schedule.Enabled
		page.ScheduleInterval = shortImportInterval(importStatus.Schedule.Interval)
	}
	for _, ref := range importStatus.Refs {
		page.Refs = append(page.Refs, webui.ImportRefRow{Name: ref.Name, State: ref.State, Source: ref.SourceOID, Local: ref.LocalOID})
	}
	page.Last = importRunRow(importStatus.LastRun)
	page.Active = importRunRow(importStatus.ActiveRun)
	cursor, _ := strconv.ParseInt(request.URL.Query().Get("cursor"), 10, 64)
	if cursor < 0 {
		cursor = 0
	}
	runs, more, err := app.Imports.HistoryBefore(request.Context(), stored.ID, 20, cursor)
	if err == nil {
		for _, run := range runs {
			row := run
			page.History = append(page.History, *importRunRow(&row))
		}
		if more && len(runs) > 0 {
			page.OlderURL = page.SelfURL + "?cursor=" + strconv.FormatInt(runs[len(runs)-1].RowID, 10)
			page.SelfURL = page.SelfURL + "?cursor=" + strconv.FormatInt(cursor, 10)
		}
	}
	_ = summary
	app.render(writer, status, page)
}

func importRunRow(run *importsync.RunView) *webui.ImportRunRow {
	if run == nil {
		return nil
	}
	return &webui.ImportRunRow{ID: run.ID, Kind: run.Kind, Status: run.Status, Message: run.Message, ErrorClass: run.ErrorClass, RowID: run.RowID}
}

func (app *App) importAdminPassword(writer http.ResponseWriter, request *http.Request, chrome *webui.Chrome) (bool, int) {
	if err := app.Auth.VerifyCredential(request.Context(), "admin", postValue(request, "admin_password"), request.RemoteAddr); err != nil {
		code, status := webui.MsgAdminFailed, http.StatusUnauthorized
		if errors.Is(err, auth.ErrRateLimited) {
			code, status = webui.MsgAdminLocked, http.StatusTooManyRequests
		}
		chrome.Notices = append(chrome.Notices, webui.Error("", code))
		return false, status
	}
	return true, http.StatusOK
}

func postedImportCredential(request *http.Request) (*importsync.Credentials, error) {
	form := postValue(request, "credential_form")
	username := postValue(request, "username")
	password := postValue(request, "password")
	token := postValue(request, "token")
	caPEM := postValue(request, "ca_pem")
	if !importCredentialFieldsPresent(form, username, password, token, caPEM) {
		return nil, nil
	}
	return importCredentialFromInput(form, username, password, token, caPEM)
}

// importFailureNotice explains a failed import action in the reader's
// language from its stable class. Internal error text is not shown here.
func importFailureNotice(err error, errorClass string) webui.Notice {
	if errorClass == "" {
		errorClass = importsyncProblemCode(err)
	}
	return webui.Error("", webui.ImportErrorCode(errorClass))
}

func shortImportInterval(value string) string {
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return value
	}
	switch {
	case parsed%time.Hour == 0:
		return strconv.FormatInt(int64(parsed/time.Hour), 10) + "h"
	case parsed%time.Minute == 0:
		return strconv.FormatInt(int64(parsed/time.Minute), 10) + "m"
	case parsed%time.Second == 0:
		return strconv.FormatInt(int64(parsed/time.Second), 10) + "s"
	default:
		return parsed.String()
	}
}

func importProblemStatus(err error) int {
	status, _, _, _ := importProblemHTTP(err)
	if status < 400 {
		return http.StatusConflict
	}
	return status
}
