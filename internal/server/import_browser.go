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
	"owngit/internal/requestctx"
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
	page.CredentialForm = postValue(request, "credential_form")
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
	// A mistake the form can name is reported on its field, before anything
	// is sent to the import service, which stays the authority on every rule.
	if problems := importNameProblems(page.Name, page.Description); len(problems) > 0 {
		chrome.Notices = problems
		page.Chrome = chrome
		app.render(writer, http.StatusUnprocessableEntity, page)
		return
	}
	if notice, ok := importURLProblem(page.URL); !ok {
		chrome.Notices = []webui.Notice{notice}
		page.Chrome = chrome
		app.render(writer, http.StatusUnprocessableEntity, page)
		return
	}
	if problems, status := importCredentialProblems(request); len(problems) > 0 {
		chrome.Notices = problems
		page.Chrome = chrome
		app.render(writer, status, page)
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
				chrome.Notices = []webui.Notice{webui.Error("name", webui.MsgRepoNameReserved)}
			} else if code := importsyncProblemCode(err); code == importsync.CodeRepositoryTaken {
				chrome.Notices = []webui.Notice{webui.Error("name", webui.MsgImportErrorRepoTaken)}
			} else if code == importsync.CodeInvalidSource {
				// The name and the address were checked above, so what is
				// left is the address as the import service reads it.
				chrome.Notices = []webui.Notice{webui.Error("url", webui.MsgImportErrorInvalidSource)}
			} else {
				chrome.Notices = []webui.Notice{importFailureNotice(err, result.Run.ErrorClass)}
			}
			page.Chrome = chrome
			app.render(writer, importProblemStatus(err), page)
			return
		}
		notice = "import_run_cancelled"
	}
	app.noticeRedirect(writer, request, "/repositories/"+url.PathEscape(result.RepositoryID)+"/import?notice="+notice, http.StatusSeeOther)
}

// handleImportPage serves the repository's Import tab. Anyone who may read
// the repository sees its import status; the source address, credential
// state and technical messages are administrator data and are filled in
// only for an administrator session. Every change is a POST that needs that
// session and the administrator password again.
func (app *App) handleImportPage(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodGet {
		// Opening the source form is a change, so it asks for the
		// administrator password first and returns here afterwards.
		if request.URL.Query().Get("setup") == "1" {
			if _, ok := app.requireBrowserAdmin(writer, request); !ok {
				return
			}
		}
		if session, admin := app.cookieSession(request, "admin", adminCookie); admin {
			var err error
			chrome, err = app.chrome(writer, request, webui.SectionRepository, stored.ID, session.CSRF)
			if err != nil {
				app.writePlainError(writer, http.StatusServiceUnavailable)
				return
			}
		}
		app.renderImportPage(writer, request, stored, summary, chrome, http.StatusOK)
		return
	}
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
		if problem, ok := importURLProblem(postValue(request, "url")); !ok {
			chrome.Notices = append(chrome.Notices, problem)
			app.renderImportPage(writer, request, stored, summary, chrome, http.StatusUnprocessableEntity)
			return
		}
		_, err = app.Imports.ConfigureSource(request.Context(), importsync.ConfigureInput{
			RepositoryID: stored.ID, URL: postValue(request, "url"), Mode: importsync.Mode(postValue(request, "mode")),
			GitOnlyConsent: postValue(request, "git_only_consent") == "1", AllowPrivateNetwork: postValue(request, "allow_private_network") == "1",
		})
		if importsyncProblemCode(err) == importsync.CodeInvalidSource {
			chrome.Notices = append(chrome.Notices, webui.Error("url", webui.MsgImportErrorInvalidSource))
			app.renderImportPage(writer, request, stored, summary, chrome, importProblemStatus(err))
			return
		}
		notice = "import_saved"
	case webui.ActionImportCredentials:
		if problems, status := importCredentialProblems(request); len(problems) > 0 {
			chrome.Notices = append(chrome.Notices, problems...)
			app.renderImportPage(writer, request, stored, summary, chrome, status)
			return
		}
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
	app.noticeRedirect(writer, request, "/repositories/"+url.PathEscape(stored.ID)+"/import?notice="+notice, status)
}

func (app *App) renderImportPage(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome, status int) {
	base := app.baseRepositoryPage(request, chrome, stored, summary)
	self := base.Repo.URL + "/import"
	admin := chrome.Viewer.AdminConfirmed
	page := webui.ImportPage{
		Chrome: chrome, Repo: base.Repo, SubmitURL: self, SelfURL: self,
		Tabs: repositoryTabs(base, webui.RepoTabImport), Admin: admin,
		AdminLoginURL: "/admin/login?next=" + url.QueryEscape(self),
		SetupURL:      "/admin/login?next=" + url.QueryEscape(self+"?setup=1"),
	}
	if admin {
		page.SetupURL = self + "?setup=1"
		// The source form opens on request, and stays open after a refused
		// change so the reader can correct it.
		page.Setup = request.URL.Query().Get("setup") == "1" ||
			(request.Method == http.MethodPost && postValue(request, "action") == webui.ActionImportConfigure)
		if request.Method == http.MethodPost {
			page.CredentialChoice = postValue(request, "credential_form")
		}
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
	page.Mode = importStatus.Mode
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
	page.Last = importRunRow(importStatus.LastRun, admin)
	page.Active = importRunRow(importStatus.ActiveRun, admin)
	if admin {
		page.URL = importStatus.URL
		page.GitOnlyConsent = importStatus.GitOnlyConsent
		page.PrivateNetwork = importStatus.TransportConsent
		page.CredentialForm = importStatus.CredentialForm
		page.CredentialBound = importStatus.CredentialBound
		page.CAPresent = importStatus.CAPresent
		if request.Method == http.MethodPost && postValue(request, "action") == webui.ActionImportConfigure {
			// A refused change shows what was typed, not the saved source.
			page.URL = strings.TrimSpace(postValue(request, "url"))
			page.Mode = postValue(request, "mode")
			page.GitOnlyConsent = postValue(request, "git_only_consent") == "1"
			page.PrivateNetwork = postValue(request, "allow_private_network") == "1"
		}
	}
	cursor, _ := strconv.ParseInt(request.URL.Query().Get("cursor"), 10, 64)
	if cursor < 0 {
		cursor = 0
	}
	runs, more, err := app.Imports.HistoryBefore(request.Context(), stored.ID, 20, cursor)
	if err == nil {
		for _, run := range runs {
			row := run
			page.History = append(page.History, *importRunRow(&row, admin))
		}
		if more && len(runs) > 0 {
			page.OlderURL = page.SelfURL + "?cursor=" + strconv.FormatInt(runs[len(runs)-1].RowID, 10)
			page.SelfURL = page.SelfURL + "?cursor=" + strconv.FormatInt(cursor, 10)
		}
	}
	_ = summary
	app.render(writer, status, page)
}

// importRunRow is one run for the page. Its technical message can name the
// source host, so it is kept for an administrator only; everyone sees the
// kind, the status and the explained failure class.
func importRunRow(run *importsync.RunView, admin bool) *webui.ImportRunRow {
	if run == nil {
		return nil
	}
	row := &webui.ImportRunRow{ID: run.ID, Kind: run.Kind, Status: run.Status, ErrorClass: run.ErrorClass, RowID: run.RowID}
	if admin {
		row.Message = run.Message
	}
	return row
}

func (app *App) importAdminPassword(writer http.ResponseWriter, request *http.Request, chrome *webui.Chrome) (bool, int) {
	if err := app.Auth.VerifyCredential(request.Context(), "admin", postValue(request, "admin_password"), requestctx.Of(request).ClientAddress); err != nil {
		code, status := webui.MsgAdminFailed, http.StatusUnauthorized
		if errors.Is(err, auth.ErrRateLimited) {
			code, status = webui.MsgAdminLocked, http.StatusTooManyRequests
		}
		chrome.Notices = append(chrome.Notices, webui.Error("", code))
		return false, status
	}
	return true, http.StatusOK
}

// browserCredentialInput is the credential part of a browser form, reduced
// to the fields of the chosen credential form. The page shows only that
// form's fields, but a browser without scripting still submits the hidden
// ones, for example a token typed before switching to Basic. Those values
// are ignored here: never validated, stored, or shown again. The JSON API
// keeps its strict rule that a request names only one kind of secret.
type browserCredentialInput struct {
	form, username, password, token, caPEM string
}

func postedCredentialInput(request *http.Request) browserCredentialInput {
	input := browserCredentialInput{form: strings.TrimSpace(postValue(request, "credential_form")), caPEM: postValue(request, "ca_pem")}
	switch input.form {
	case "basic":
		input.username, input.password = postValue(request, "username"), postValue(request, "password")
	case "bearer":
		input.token = postValue(request, "token")
	}
	return input
}

func postedImportCredential(request *http.Request) (*importsync.Credentials, error) {
	input := postedCredentialInput(request)
	if !importCredentialFieldsPresent(input.form, input.username, input.password, input.token, input.caPEM) {
		return nil, nil
	}
	return importCredentialFromInput(input.form, input.username, input.password, input.token, input.caPEM)
}

// importNameProblems reports a new import's name or description that the
// repository rules refuse, on its own field. An empty name is allowed: the
// import then derives one from the source address.
func importNameProblems(name, description string) []webui.Notice {
	if name == "" {
		if len(description) > 500 {
			return []webui.Notice{webui.Error("description", webui.MsgRepoDescriptionTooLong)}
		}
		return nil
	}
	switch err := repository.ValidateName(name, description); {
	case err == nil:
		return nil
	case errors.Is(err, repository.ErrReservedName):
		return []webui.Notice{webui.Error("name", webui.MsgRepoNameReserved)}
	case errors.Is(err, repository.ErrInvalidDescription):
		return []webui.Notice{webui.Error("description", webui.MsgRepoDescriptionTooLong)}
	default:
		return []webui.Notice{webui.Error("name", webui.MsgRepoNameInvalid)}
	}
}

// importURLProblem names the rule a source address breaks, on the address
// field. The import service applies the full rules afterwards; this only
// turns the common mistakes into a sentence that says what to change.
func importURLProblem(raw string) (webui.Notice, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return webui.Error("url", webui.MsgImportURLRequired), false
	}
	parsed, err := url.Parse(raw)
	switch {
	case err != nil:
		return webui.Error("url", webui.MsgImportErrorInvalidSource), false
	case parsed.Scheme != "https":
		return webui.Error("url", webui.MsgImportURLHTTPS), false
	case parsed.User != nil:
		return webui.Error("url", webui.MsgImportURLUser), false
	case parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "":
		return webui.Error("url", webui.MsgImportURLQuery), false
	case parsed.Host == "":
		return webui.Error("url", webui.MsgImportErrorInvalidSource), false
	}
	return webui.Notice{}, true
}

// importCredentialProblems names, on its field, what the chosen credential
// form is missing, with the status to answer. Fields of the other forms were
// already dropped by postedCredentialInput. A form the page never offers can
// only come from a crafted request, so it is answered as a bad request, still
// with the field note.
func importCredentialProblems(request *http.Request) ([]webui.Notice, int) {
	input := postedCredentialInput(request)
	var problems []webui.Notice
	switch input.form {
	case "basic":
		if input.username == "" {
			problems = append(problems, webui.Error("username", webui.MsgImportBasicNeedsBoth))
		}
		if input.password == "" {
			problems = append(problems, webui.Error("password", webui.MsgImportBasicNeedsBoth))
		}
	case "bearer":
		if input.token == "" {
			problems = append(problems, webui.Error("token", webui.MsgImportTokenRequired))
		}
	case "", "none":
	default:
		return []webui.Notice{webui.Error("credential_form", webui.MsgImportCredentialFormUnknown)}, http.StatusBadRequest
	}
	return problems, http.StatusUnprocessableEntity
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
