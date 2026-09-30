package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"owngit/internal/importfetch"
	"owngit/internal/importsync"
	"owngit/internal/repository"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func (app *App) handleNewImport(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	session, ok := app.requireAdminPage(writer, request)
	if !ok {
		return
	}
	chrome, err := app.chrome(writer, request, webui.SectionOverview, "", session.CSRF)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	page := webui.NewImportPage{Chrome: chrome, SubmitURL: "/repositories/new-import", Options: importOptionsForm(nil, nil, nil)}
	if request.Method == http.MethodGet {
		app.render(writer, request, http.StatusOK, page)
		return
	}
	if !app.parseForm(writer, request) {
		return
	}
	page.Name = strings.TrimSpace(postValue(request, "name"))
	page.Description = strings.TrimSpace(postValue(request, "description"))
	page.URL = strings.TrimSpace(postValue(request, "url"))
	page.Mode = postValue(request, "mode")
	page.GitOnlyConsent = postValue(request, "git_only_consent") == "1"
	page.PrivateNetwork = postValue(request, "allow_private_network") == "1"
	if !app.requireCSRF(writer, request) {
		return
	}
	page.CredentialForm = postValue(request, "credential_form")
	posted := readPostedImportOptions(request)
	// render shows the form again as typed, with its notices.
	render := func(status int, notices ...webui.Notice) {
		chrome.Notices = append(chrome.Notices, notices...)
		page.Chrome = chrome
		page.Options = importOptionsForm(nil, &posted, chrome.Notices)
		app.render(writer, request, status, page)
	}
	if ok, status := app.confirmImportAdmin(writer, request, &chrome); !ok {
		render(status)
		return
	}
	// A mistake the form can name is reported on its field, before anything
	// is sent to the import service, which stays the authority on every rule.
	if problems := importNameProblems(page.Name, page.Description); len(problems) > 0 {
		render(http.StatusUnprocessableEntity, problems...)
		return
	}
	if notice, ok := importURLProblem(page.URL, posted.plainHTTP); !ok {
		render(http.StatusUnprocessableEntity, notice)
		return
	}
	options, problems := posted.change()
	if len(problems) > 0 {
		render(http.StatusUnprocessableEntity, problems...)
		return
	}
	if problems, status := importCredentialProblems(request); len(problems) > 0 {
		render(status, problems...)
		return
	}
	credential, credErr := postedImportCredential(request)
	if credErr != nil {
		render(http.StatusUnprocessableEntity, importFailureNotice(credErr, ""))
		return
	}
	limits := app.sourceRunLimits(options.Limits["run_seconds"])
	request = app.beginImportRun(writer, request, limits.RunTimeout)
	result, err := app.Imports.Import(request.Context(), importsync.ImportInput{
		Name: page.Name, Description: page.Description, URL: page.URL, Mode: importsync.Mode(page.Mode),
		GitOnlyConsent: page.GitOnlyConsent, AllowPrivateNetwork: page.PrivateNetwork, Credentials: credential,
		Options: options, Limits: limits,
	})
	app.noteImportOrigin(request, result.Run)
	notice := "import_started"
	if err != nil {
		// A cancelled run may have added the repository before it stopped.
		cancelled := importsyncProblemCode(err) == importsync.CodeCancelled
		repositoryExists := false
		if cancelled && result.RepositoryID != "" {
			var lookupErr error
			_, repositoryExists, lookupErr = app.Store.Repository(request.Context(), result.RepositoryID)
			if lookupErr != nil {
				// Whether it did is unknown, so the form says so rather than
				// stating either outcome.
				render(unavailable(request, "repository record read", lookupErr), webui.Error("", webui.MsgImportCancelledUnsure))
				return
			}
		}
		if !repositoryExists {
			// Nothing to show on a repository page: keep the form and explain.
			var notice webui.Notice
			if cancelled {
				notice = webui.Notice{Kind: webui.NoticeWarning, Code: webui.MsgImportCancelledNoRepo}
			} else if errors.Is(err, repository.ErrReservedName) {
				notice = webui.Error("name", webui.MsgRepoNameReserved)
			} else if code := importsyncProblemCode(err); code == importsync.CodeRepositoryTaken {
				notice = webui.Error("name", webui.MsgImportErrorRepoTaken)
			} else if code == importsync.CodeInvalidSource {
				// The name and the form's own mistakes were checked above, so
				// what is left is the source as the import service reads it.
				notice = importSourceProblem(err)
			} else {
				notice = importFailureNotice(err, result.Run.ErrorClass)
			}
			render(importProblemStatus(request, "import start", err), notice)
			return
		}
		notice = "import_run_cancelled"
	}
	app.noticeRedirect(writer, request, "/repositories/"+url.PathEscape(result.RepositoryID)+"/import?notice="+notice, http.StatusSeeOther)
}

// handleImportPage serves the repository's Import tab. Anyone who may read
// the repository sees its import status; the source address, credential
// state and technical messages are administrator data and are filled in
// only for a viewer who may open the administrator pages. Every change is a
// POST that passes requireAdminPage and confirmAdmin.
func (app *App) handleImportPage(writer http.ResponseWriter, request *http.Request, stored state.Repository, summary repository.Summary, chrome webui.Chrome) {
	writer.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodGet {
		// Opening the source form is a change, so it asks for the
		// administrator password first and returns here afterwards.
		if request.URL.Query().Get("setup") == "1" {
			if _, ok := app.requireAdminPage(writer, request); !ok {
				return
			}
		}
		app.renderImportPage(writer, request, stored, summary, chrome, http.StatusOK)
		return
	}
	session, ok := app.requireAdminPage(writer, request)
	if !ok {
		return
	}
	var err error
	chrome, err = app.chrome(writer, request, webui.SectionRepository, stored.ID, session.CSRF)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	if !app.parseForm(writer, request) {
		return
	}
	if !app.requireCSRF(writer, request) {
		return
	}
	if ok, status := app.confirmImportAdmin(writer, request, &chrome); !ok {
		app.renderImportPage(writer, request, stored, summary, chrome, status)
		return
	}
	notice := "import_saved"
	status := http.StatusSeeOther
	refresh := false
	switch postValue(request, "action") {
	case webui.ActionImportConfigure:
		posted := readPostedImportOptions(request)
		if problem, ok := importURLProblem(postValue(request, "url"), posted.plainHTTP); !ok {
			chrome.Notices = append(chrome.Notices, problem)
			app.renderImportPage(writer, request, stored, summary, chrome, http.StatusUnprocessableEntity)
			return
		}
		options, problems := posted.change()
		if len(problems) > 0 {
			chrome.Notices = append(chrome.Notices, problems...)
			app.renderImportPage(writer, request, stored, summary, chrome, http.StatusUnprocessableEntity)
			return
		}
		_, err = app.Imports.ConfigureSource(request.Context(), importsync.ConfigureInput{
			RepositoryID: stored.ID, URL: postValue(request, "url"), Mode: importsync.Mode(postValue(request, "mode")),
			GitOnlyConsent: postValue(request, "git_only_consent") == "1", AllowPrivateNetwork: postValue(request, "allow_private_network") == "1",
			// The connection choices were drawn for another address (without
			// scripting; owngit.js clears them when the address is edited),
			// so only the ones changed in this submission are for the new one.
			Options: options, RepeatsSaved: posted.forURL != "" && posted.forURL != strings.TrimSpace(postValue(request, "url")),
		})
		if importsyncProblemCode(err) == importsync.CodeInvalidSource {
			chrome.Notices = append(chrome.Notices, importSourceProblem(err))
			app.renderImportPage(writer, request, stored, summary, chrome, importProblemStatus(request, "import change", err))
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
		var saved state.ImportLimits
		if saved, err = app.Imports.SavedLimits(request.Context(), stored.ID); err != nil {
			refresh = true
			break
		}
		limits := app.sourceRunLimits(saved.RunSeconds)
		request = app.beginImportRun(writer, request, limits.RunTimeout)
		var run state.ImportRun
		run, err = app.Imports.Refresh(request.Context(), stored.ID, limits)
		app.noteImportOrigin(request, run)
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
		app.renderImportPage(writer, request, stored, summary, chrome, importProblemStatus(request, "import change", err))
		return
	}
	app.noticeRedirect(writer, request, "/repositories/"+url.PathEscape(stored.Address)+"/import?notice="+notice, status)
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
	importStatus, err := app.Imports.Status(request.Context(), stored.ID)
	if err != nil {
		page.StatusUnreadable = true
		app.render(writer, request, unavailable(request, "import status read", err), page)
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
		page.Options = importOptionsForm(importStatus.Options, nil, chrome.Notices)
		page.Options.ForURL = importStatus.URL
		page.Options.Effects = importRefreshEffects(importStatus.RefreshEffects)
		page.Options.EffectsUnknown = importStatus.RefreshEffectsUnknown
		page.OptionsSummary = importOptionFacts(importStatus.Options)
		page.RefreshSummary = importRefreshFacts(importStatus.Options)
		page.OptionsProblem = importStatus.Options != nil && importStatus.Options.Problem != ""
		page.CredentialForm = importStatus.CredentialForm
		page.CredentialBound = importStatus.CredentialBound
		page.CAPresent = importStatus.CAPresent
		if request.Method == http.MethodPost && postValue(request, "action") == webui.ActionImportConfigure {
			// A refused change shows what was typed, not the saved source.
			page.URL = strings.TrimSpace(postValue(request, "url"))
			page.Mode = postValue(request, "mode")
			page.GitOnlyConsent = postValue(request, "git_only_consent") == "1"
			page.PrivateNetwork = postValue(request, "allow_private_network") == "1"
			posted := readPostedImportOptions(request)
			posted.forAddress(importStatus.Options, page.URL)
			page.Options = importOptionsForm(importStatus.Options, &posted, chrome.Notices)
			page.Options.ForURL = posted.forURL
			page.Options.Effects = importRefreshEffects(importStatus.RefreshEffects)
			page.Options.EffectsUnknown = importStatus.RefreshEffectsUnknown
		}
	}
	cursor, _ := strconv.ParseInt(request.URL.Query().Get("cursor"), 10, 64)
	if cursor < 0 {
		cursor = 0
	}
	// History that could not be read is reported as such beside the status
	// above, never as no runs.
	runs, more, err := app.Imports.HistoryBefore(request.Context(), stored.ID, 20, cursor)
	page.HistoryAvailable = err == nil
	if err != nil {
		logFailure(request, "import history read", err)
	}
	for _, run := range runs {
		row := run
		page.History = append(page.History, *importRunRow(&row, admin))
	}
	if err == nil && more && len(runs) > 0 {
		page.OlderURL = page.SelfURL + "?cursor=" + strconv.FormatInt(runs[len(runs)-1].RowID, 10)
		page.SelfURL = page.SelfURL + "?cursor=" + strconv.FormatInt(cursor, 10)
	}
	_ = summary
	app.render(writer, request, status, page)
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

func (app *App) confirmImportAdmin(writer http.ResponseWriter, request *http.Request, chrome *webui.Chrome) (bool, int) {
	if _, err := app.confirmAdmin(writer, request, chrome, false); err != nil {
		notice, status := adminPasswordNotice(request, err, "")
		chrome.Notices = append(chrome.Notices, notice)
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
		if len(description) > state.MaximumRepositoryDescriptionBytes {
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
// plainHTTP is the form's plain HTTP choice.
func importURLProblem(raw string, plainHTTP bool) (webui.Notice, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return webui.Error("url", webui.MsgImportURLRequired), false
	}
	parsed, err := url.Parse(raw)
	switch {
	case err != nil:
		return webui.Error("url", webui.MsgImportErrorInvalidSource), false
	case parsed.Scheme == "http" && !plainHTTP:
		return webui.Error("url", webui.MsgImportURLPlainHTTP), false
	case parsed.Scheme != "https" && parsed.Scheme != "http":
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

// importProblemStatus is the status of an import page answering err as step
// of request. A problem without an error status, a cancellation, is a
// conflict with what the page asked for.
func importProblemStatus(request *http.Request, step string, err error) int {
	status, _, _, _ := importProblemHTTP(request, step, err)
	if status < 400 {
		return http.StatusConflict
	}
	return status
}

// postedImportOptions is the connection, refs and limits group of a
// submitted source form, as typed.
type postedImportOptions struct {
	plainHTTP bool
	redirects string
	origin    string
	reserved  bool
	limits    map[string]webui.LimitInput
	// overwrite, follow and prefixes are the refresh choices; prefixes is
	// the typed list of extra ref namespaces.
	overwrite bool
	follow    bool
	prefixes  string
	// forURL is the address the connection choices were drawn or chosen
	// for, on the Import tab; empty on the new-import form.
	forURL string
}

func readPostedImportOptions(request *http.Request) postedImportOptions {
	posted := postedImportOptions{
		plainHTTP: postValue(request, "allow_plain_http") == "1",
		redirects: postValue(request, "redirects"),
		origin:    strings.TrimSpace(postValue(request, "approved_redirect_origin")),
		reserved:  postValue(request, "allow_reserved_addresses") == "1",
		limits:    map[string]webui.LimitInput{},
		overwrite: postValue(request, "overwrite_diverged") == "1",
		follow:    postValue(request, "follow_upstream_deletions") == "1",
		prefixes:  postValue(request, "extra_ref_prefixes"),
		forURL:    strings.TrimSpace(postValue(request, "options_url")),
	}
	if posted.redirects == "" {
		posted.redirects = state.ImportRedirectRefuse
	}
	for _, name := range webui.ImportLimitFieldNames() {
		posted.limits[name] = webui.LimitInput{Amount: postValue(request, name), Unit: postValue(request, name+"_unit")}
	}
	return posted
}

// forAddress binds a refused submission, redrawn for correction, to the
// address it submitted. Choices that repeat the saved ones for another
// address are drawn reset, as the service would have applied them, and
// choices changed in the submission stay, so saving the redrawn form gives
// the new address exactly what the owner sees.
func (posted *postedImportOptions) forAddress(saved *importsync.OptionsStatus, address string) {
	if posted.forURL != "" && posted.forURL != address && saved != nil {
		change := importsync.OptionsChange{
			AllowPlainHTTP: &posted.plainHTTP, Redirects: &posted.redirects, ApprovedRedirectOrigin: &posted.origin, AllowReservedAddresses: &posted.reserved,
			OverwriteDiverged: &posted.overwrite, FollowUpstreamDeletions: &posted.follow,
		}.WithoutRepeated(state.ImportSource{
			Options: state.ImportOptions{
				AllowPlainHTTP: saved.AllowPlainHTTP, Redirects: saved.Redirects, ApprovedRedirectOrigin: saved.ApprovedRedirectOrigin, AllowReservedAddresses: saved.AllowReservedAddresses,
			},
			OverwriteDiverged: saved.OverwriteDiverged, FollowUpstreamDeletions: saved.FollowUpstreamDeletions,
		})
		defaults := state.DefaultImportOptions()
		if change.AllowPlainHTTP == nil {
			posted.plainHTTP = defaults.AllowPlainHTTP
		}
		if change.Redirects == nil {
			posted.redirects, posted.origin = defaults.Redirects, defaults.ApprovedRedirectOrigin
		}
		if change.AllowReservedAddresses == nil {
			posted.reserved = defaults.AllowReservedAddresses
		}
		if change.OverwriteDiverged == nil {
			posted.overwrite = false
		}
		if change.FollowUpstreamDeletions == nil {
			posted.follow = false
		}
	}
	posted.forURL = address
}

// change turns the form into an options change. The form carries every
// option, so the change sets each one, and an empty limit returns to its
// default. A mistake the form can name is returned on its field, before
// anything reaches the import service, which stays the authority.
func (posted postedImportOptions) change() (importsync.OptionsChange, []webui.Notice) {
	prefixes := strings.FieldsFunc(posted.prefixes, func(character rune) bool { return character == ',' || unicode.IsSpace(character) })
	change := importsync.OptionsChange{
		AllowPlainHTTP: &posted.plainHTTP, Redirects: &posted.redirects, AllowReservedAddresses: &posted.reserved,
		Limits: map[string]int64{}, OverwriteDiverged: &posted.overwrite, FollowUpstreamDeletions: &posted.follow, ExtraRefPrefixes: &prefixes,
	}
	var problems []webui.Notice
	if state.ValidateExtraRefPrefixes(prefixes) != nil {
		problems = append(problems, webui.Error("extra_ref_prefixes", webui.MsgImportExtraRefsInvalid))
	}
	switch posted.redirects {
	case state.ImportRedirectRefuse, state.ImportRedirectSameOrigin:
		// An origin typed but unused is still refused when malformed; a saved
		// one shown again is dropped with the policy change.
		if posted.origin != "" {
			if _, err := importfetch.ParseRedirectOrigin(posted.origin, true); err != nil {
				problems = append(problems, webui.Error("approved_redirect_origin", webui.MsgImportOriginInvalid))
			}
		}
	case state.ImportRedirectApproved:
		if _, err := importfetch.ParseRedirectOrigin(posted.origin, posted.plainHTTP); err != nil {
			problems = append(problems, webui.Error("approved_redirect_origin", webui.MsgImportOriginInvalid))
		}
		change.ApprovedRedirectOrigin = &posted.origin
	default:
		problems = append(problems, webui.Error("redirects", webui.MsgImportErrorInvalidSource))
	}
	defaults := importsync.DefaultSourceLimits()
	for _, name := range webui.ImportLimitFieldNames() {
		value, err := webui.ParseImportLimit(name, posted.limits[name])
		if err != nil {
			problems = append(problems, webui.Error(name, webui.ImportLimitNotice(name, err)))
			continue
		}
		if value == 0 {
			standard, _ := defaults.Field(name)
			value = *standard
		}
		if state.CheckImportLimit(name, value) != nil {
			problems = append(problems, webui.Error(name, webui.MsgImportLimitRange))
			continue
		}
		change.Limits[name] = value
	}
	return change, problems
}

// importOptionFields names the fields of the options group, so a refusal on
// one of them opens the group.
func importOptionField(field string) bool {
	switch field {
	case "allow_plain_http", "redirects", "approved_redirect_origin", "allow_reserved_addresses",
		"extra_ref_prefixes", "overwrite_diverged", "follow_upstream_deletions":
		return true
	}
	for _, name := range webui.ImportLimitFieldNames() {
		if field == name {
			return true
		}
	}
	return false
}

// importOptionsForm draws the connection and limits group: the saved
// options, or a refused submission as typed when posted is not nil.
func importOptionsForm(saved *importsync.OptionsStatus, posted *postedImportOptions, notices []webui.Notice) webui.ImportOptionsForm {
	if saved == nil {
		saved = importsync.DescribeOptions(state.ImportSource{Options: state.DefaultImportOptions()}, nil)
	}
	changed := map[string]int64{}
	for _, name := range saved.ChangedLimits {
		value, _ := saved.Limits.Field(name)
		changed[name] = *value
	}
	defaults := importsync.DefaultSourceLimits()
	bounds := map[string]webui.ImportLimitBounds{}
	for _, field := range state.ImportLimitFields {
		standard, _ := defaults.Field(field.Name)
		bounds[field.Name] = webui.ImportLimitBounds{Min: field.Min, Max: field.Max, Default: *standard}
	}
	form := webui.ImportOptionsForm{
		PlainHTTP: saved.AllowPlainHTTP, Redirects: saved.Redirects, ApprovedOrigin: saved.ApprovedRedirectOrigin, Reserved: saved.AllowReservedAddresses,
		Overwrite: saved.OverwriteDiverged, Follow: saved.FollowUpstreamDeletions, ExtraRefPrefixes: strings.Join(saved.ExtraRefPrefixes, "\n"),
	}
	var typed map[string]webui.LimitInput
	if posted != nil {
		form.PlainHTTP, form.Redirects, form.ApprovedOrigin, form.Reserved = posted.plainHTTP, posted.redirects, posted.origin, posted.reserved
		form.Overwrite, form.Follow, form.ExtraRefPrefixes = posted.overwrite, posted.follow, posted.prefixes
		typed = posted.limits
	}
	form.Limits = webui.NewImportLimitControls(changed, typed, bounds)
	form.Open = form.PlainHTTP || form.Redirects != state.ImportRedirectRefuse || form.Reserved || len(changed) > 0 ||
		form.Overwrite || form.Follow || strings.TrimSpace(form.ExtraRefPrefixes) != ""
	for _, notice := range notices {
		if importOptionField(notice.Field) {
			form.Open = true
			if form.Refused == "" {
				form.Refused = notice.Field
			}
		}
	}
	return form
}

// importOptionFacts lists the options that differ from their defaults, for
// the status strip.
func importOptionFacts(options *importsync.OptionsStatus) []webui.ImportOptionFact {
	if options == nil {
		return nil
	}
	var facts []webui.ImportOptionFact
	if options.AllowPlainHTTP {
		facts = append(facts, webui.ImportOptionFact{Code: webui.MsgImportFactPlainHTTP})
	}
	switch options.Redirects {
	case state.ImportRedirectSameOrigin:
		facts = append(facts, webui.ImportOptionFact{Code: webui.MsgImportFactSameOrigin})
	case state.ImportRedirectApproved:
		facts = append(facts, webui.ImportOptionFact{Code: webui.MsgImportFactApproved, Value: options.ApprovedRedirectOrigin})
	}
	if options.AllowReservedAddresses {
		facts = append(facts, webui.ImportOptionFact{Code: webui.MsgImportFactReserved})
	}
	if len(options.ChangedLimits) > 0 {
		facts = append(facts, webui.ImportOptionFact{Code: webui.MsgImportFactLimits, Value: strconv.Itoa(len(options.ChangedLimits))})
	}
	return facts
}

// importRefreshFacts lists the refresh choices that differ from their
// defaults, for the status strip.
func importRefreshFacts(options *importsync.OptionsStatus) []webui.ImportOptionFact {
	if options == nil {
		return nil
	}
	var facts []webui.ImportOptionFact
	if len(options.ExtraRefPrefixes) > 0 {
		facts = append(facts, webui.ImportOptionFact{Code: webui.MsgImportFactExtraRefs, Value: strings.Join(options.ExtraRefPrefixes, ", ")})
	}
	if options.OverwriteDiverged {
		facts = append(facts, webui.ImportOptionFact{Code: webui.MsgImportFactOverwrite})
	}
	if options.FollowUpstreamDeletions {
		facts = append(facts, webui.ImportOptionFact{Code: webui.MsgImportFactFollowDeletions})
	}
	return facts
}

// importRefreshEffects lists the local refs the refresh choices would change
// now, for the source form.
func importRefreshEffects(effects []importsync.RefreshEffect) []webui.ImportRefreshEffect {
	rows := make([]webui.ImportRefreshEffect, 0, len(effects))
	for _, effect := range effects {
		rows = append(rows, webui.ImportRefreshEffect{Name: effect.Name, Effect: effect.Effect, LocalChanged: effect.LocalChanged, History: effect.History})
	}
	return rows
}

// importSourceProblem names, on its field, why the import service refused a
// source configuration.
func importSourceProblem(err error) webui.Notice {
	var conflict *importsync.LimitConflictError
	var outOfRange *state.ImportLimitRangeError
	switch {
	case errors.Is(err, importfetch.ErrPlainHTTP):
		return webui.Error("url", webui.MsgImportURLPlainHTTP)
	case errors.As(err, &conflict):
		return webui.Error(conflict.Field, webui.MsgImportLimitRange)
	case errors.As(err, &outOfRange):
		return webui.Error(outOfRange.Field.Name, webui.MsgImportLimitRange)
	}
	return webui.Error("url", webui.MsgImportErrorInvalidSource)
}
