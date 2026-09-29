package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"owngit/internal/importsync"
	"owngit/internal/pullrequest"
	"owngit/internal/state"
)

func importHistoryQueryAllowed(request *http.Request, repositoryRoute bool, resource, remainder string) bool {
	if !repositoryRoute || resource != "import" || remainder != "history" || request.Method != http.MethodGet {
		return false
	}
	for key := range request.URL.Query() {
		if key != "cursor" && key != "limit" {
			return false
		}
	}
	return true
}

func (app *App) handleImportAPI(writer http.ResponseWriter, request *http.Request, repositoryID, remainder string) {
	if !app.authorizeAdminAPI(writer, request) {
		return
	}
	// Cancelling needs no repository data and stays available.
	if remainder != "cancel" && app.refusePreparingAPI(writer, request, repositoryID) {
		return
	}
	switch remainder {
	case "":
		app.handleImportSourceAPI(writer, request, repositoryID)
	case "run":
		app.handleImportRunAPI(writer, request, repositoryID)
	case "cancel":
		app.handleImportCancelAPI(writer, request, repositoryID)
	case "resolve":
		app.handleImportResolveAPI(writer, request, repositoryID)
	case "history":
		app.handleImportHistoryAPI(writer, request, repositoryID)
	case "schedule":
		app.handleImportScheduleAPI(writer, request, repositoryID)
	case "credentials":
		app.handleImportCredentialsAPI(writer, request, repositoryID)
	default:
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
	}
}

func (app *App) handleImportSourceAPI(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	if !app.importRepositoryExists(writer, request, repositoryID) {
		return
	}
	switch request.Method {
	case http.MethodGet:
		status, err := app.Imports.Status(request.Context(), repositoryID)
		if err != nil {
			writeImportProblem(writer, request, "import status read", err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, struct {
			OK     bool              `json:"ok"`
			Status importsync.Status `json:"status"`
		}{OK: true, Status: status})
	case http.MethodPut:
		var input struct {
			URL                 string `json:"url"`
			Mode                string `json:"mode"`
			GitOnlyConsent      bool   `json:"git_only_consent"`
			AllowPrivateNetwork bool   `json:"allow_private_network"`
		}
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		source, err := app.Imports.ConfigureSource(request.Context(), importsync.ConfigureInput{
			RepositoryID: repositoryID, URL: input.URL, Mode: importsync.Mode(input.Mode),
			GitOnlyConsent: input.GitOnlyConsent, AllowPrivateNetwork: input.AllowPrivateNetwork,
		})
		if err != nil {
			writeImportProblem(writer, request, "import source change", err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, importSourceJSON(source))
	default:
		writeAPIMethodError(writer, http.MethodGet+", "+http.MethodPut)
	}
}

func (app *App) handleImportRunAPI(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	if request.Method != http.MethodPost {
		writeAPIMethodError(writer, http.MethodPost)
		return
	}
	var input struct {
		Name                string `json:"name"`
		Description         string `json:"description"`
		URL                 string `json:"url"`
		Mode                string `json:"mode"`
		GitOnlyConsent      bool   `json:"git_only_consent"`
		AllowPrivateNetwork bool   `json:"allow_private_network"`
		CredentialForm      string `json:"credential_form"`
		Username            string `json:"username"`
		Password            string `json:"password"`
		Token               string `json:"token"`
		CAPEM               string `json:"ca_pem"`
	}
	if !decodeAPIJSONLimit(writer, request, &input, MaximumImportCredentialRequest) {
		return
	}
	request = app.beginOperation(writer, request)
	_, _, exists, err := app.Repositories.ExistingPath(request.Context(), repositoryID)
	if err != nil {
		writeAPIError(writer, unavailable(request, "repository storage read", err), importsync.CodeStateUnavailable, "The repository destination could not be read.", nil)
		return
	}
	// A request that names a source is a new import. It must not turn into a
	// refresh of whatever source an existing repository has, and a refresh
	// must not turn into a new import without a source.
	addShaped := input.URL != "" || input.Name != "" || input.Mode != "" || input.Description != "" || input.GitOnlyConsent || input.AllowPrivateNetwork
	credentialsPresent := importCredentialFieldsPresent(input.CredentialForm, input.Username, input.Password, input.Token, input.CAPEM)
	if exists {
		if addShaped {
			writeAPIError(writer, http.StatusConflict, importsync.CodeRepositoryTaken, "The repository already exists, so nothing was imported. Use refresh to update it from its stored source, or change the source on the repository's Import tab.", nil)
			return
		}
		if credentialsPresent {
			writeAPIError(writer, http.StatusUnprocessableEntity, importsync.CodeInvalidSource, "Refresh does not accept credentials. Save them with the credentials endpoint first.", nil)
			return
		}
		run, runErr := app.Imports.Refresh(request.Context(), repositoryID, app.importRunLimits())
		app.writeImportRunResult(writer, request, repositoryID, run, runErr)
		return
	}
	if !addShaped && !credentialsPresent {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist, so there is nothing to refresh. Use import add with a source URL to create it.", nil)
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = repositoryID
	}
	if strings.ToLower(name) != repositoryID {
		writeAPIError(writer, http.StatusUnprocessableEntity, importsync.CodeInvalidSource, "The import name does not match the repository identifier.", nil)
		return
	}
	var credential *importsync.Credentials
	if credentialsPresent {
		parsed, credErr := importCredentialFromInput(input.CredentialForm, input.Username, input.Password, input.Token, input.CAPEM)
		if credErr != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, importsync.CodeInvalidSource, credErr.Error(), nil)
			return
		}
		credential = parsed
	}
	result, runErr := app.Imports.Import(request.Context(), importsync.ImportInput{
		Name: name, Description: input.Description, URL: input.URL, Mode: importsync.Mode(input.Mode),
		GitOnlyConsent: input.GitOnlyConsent, AllowPrivateNetwork: input.AllowPrivateNetwork, Credentials: credential, Limits: app.importRunLimits(),
	})
	app.writeImportRunResult(writer, request, result.RepositoryID, result.Run, runErr)
}

// writeImportRunResult reports a finished or cancelled run with its
// repository and run record, and any other outcome as its error.
func (app *App) writeImportRunResult(writer http.ResponseWriter, request *http.Request, repositoryID string, run state.ImportRun, runErr error) {
	cancelled := importsyncProblemCode(runErr) == importsync.CodeCancelled
	if runErr != nil && !cancelled {
		writeImportProblem(writer, request, "import run", runErr)
		return
	}
	body := struct {
		OK           bool                          `json:"ok"`
		Code         string                        `json:"code,omitempty"`
		RepositoryID string                        `json:"repository_id,omitempty"`
		Run          importsync.RunView            `json:"run"`
		Status       *importsync.Status            `json:"status"`
		StatusError  *pullrequest.ErrorDescription `json:"status_error,omitempty"`
	}{OK: true, RepositoryID: repositoryID, Run: importsync.RunRecordView(run)}
	if cancelled {
		body.Code = importsync.CodeCancelled
	}
	body.Status, body.StatusError = app.importStatusAfter(request, repositoryID)
	writeAPIJSON(writer, http.StatusOK, body)
}

// importStatusAfter reads the current import status for a response that
// already reports a committed outcome. The read can fail on its own; then the
// status is nil and the reason says why, since a guessed status would
// misdescribe the import and an error would hide what was committed. Status
// reports every failure as an import problem, so the reason is its own code
// and safe message.
func (app *App) importStatusAfter(request *http.Request, repositoryID string) (*importsync.Status, *pullrequest.ErrorDescription) {
	status, err := app.Imports.Status(request.Context(), repositoryID)
	if err != nil {
		_, code, message, _ := importProblemHTTP(request, "import status read", err)
		return nil, &pullrequest.ErrorDescription{Code: code, Message: message}
	}
	return &status, nil
}

func (app *App) handleImportCancelAPI(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	if request.Method != http.MethodPost {
		writeAPIMethodError(writer, http.MethodPost)
		return
	}
	// A first import has no repository until it finishes, so a name without a
	// repository still reaches its running import.
	_, exists, err := app.Store.Repository(request.Context(), repositoryID)
	if err != nil {
		writeAPIError(writer, unavailable(request, "repository record read", err), importsync.CodeStateUnavailable, "OwnGit state is unavailable.", nil)
		return
	}
	if !decodeAPIJSON(writer, request, &struct{}{}) {
		return
	}
	cancelled, err := app.Imports.Cancel(request.Context(), repositoryID)
	if err != nil {
		writeImportProblem(writer, request, "import cancel", err)
		return
	}
	if !cancelled && !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist, and no import for that name is running.", nil)
		return
	}
	writeAPIJSON(writer, http.StatusOK, struct {
		OK        bool `json:"ok"`
		Cancelled bool `json:"cancelled"`
	}{OK: true, Cancelled: cancelled})
}

// handleImportResolveAPI records the owner's acceptance of the destination
// as found after an unresolved publication. It never writes to Git.
func (app *App) handleImportResolveAPI(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	if request.Method != http.MethodPost {
		writeAPIMethodError(writer, http.MethodPost)
		return
	}
	if !app.importRepositoryExists(writer, request, repositoryID) {
		return
	}
	if !decodeAPIJSON(writer, request, &struct{}{}) {
		return
	}
	result, err := app.Imports.ResolveUnresolved(request.Context(), repositoryID)
	if err != nil {
		writeImportProblem(writer, request, "import resolution", err)
		return
	}
	status, statusErr := app.importStatusAfter(request, repositoryID)
	writeAPIJSON(writer, http.StatusOK, struct {
		OK          bool                          `json:"ok"`
		Resolved    []string                      `json:"resolved"`
		Status      *importsync.Status            `json:"status"`
		StatusError *pullrequest.ErrorDescription `json:"status_error,omitempty"`
	}{OK: true, Resolved: result.Resolved, Status: status, StatusError: statusErr})
}

func (app *App) handleImportHistoryAPI(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	if request.Method != http.MethodGet {
		writeAPIMethodError(writer, http.MethodGet)
		return
	}
	if !app.importRepositoryExists(writer, request, repositoryID) {
		return
	}
	limit := 20
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeAPIError(writer, http.StatusBadRequest, "invalid_request", "History limit must be a positive integer.", nil)
			return
		}
		limit = parsed
	}
	var cursor int64
	if raw := request.URL.Query().Get("cursor"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeAPIError(writer, http.StatusBadRequest, "invalid_request", "History cursor must be a non-negative integer.", nil)
			return
		}
		cursor = parsed
	}
	runs, more, err := app.Imports.HistoryBefore(request.Context(), repositoryID, limit, cursor)
	if err != nil {
		writeImportProblem(writer, request, "import history read", err)
		return
	}
	var next int64
	if len(runs) > 0 {
		next = runs[len(runs)-1].RowID
	}
	writeAPIJSON(writer, http.StatusOK, struct {
		OK     bool                 `json:"ok"`
		Runs   []importsync.RunView `json:"runs"`
		More   bool                 `json:"more"`
		Cursor int64                `json:"cursor,omitempty"`
	}{OK: true, Runs: runs, More: more, Cursor: next})
}

func (app *App) handleImportScheduleAPI(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	if !app.importRepositoryExists(writer, request, repositoryID) {
		return
	}
	switch request.Method {
	case http.MethodGet:
		schedule, exists, err := app.Imports.Schedule(request.Context(), repositoryID)
		if err != nil {
			writeImportProblem(writer, request, "import schedule read", err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, importScheduleJSON(schedule, exists))
	case http.MethodPut:
		var input struct {
			Enabled  bool   `json:"enabled"`
			Interval string `json:"interval"`
		}
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		interval, err := time.ParseDuration(input.Interval)
		if err != nil || interval <= 0 {
			writeAPIError(writer, http.StatusUnprocessableEntity, importsync.CodeInvalidSchedule, "Schedule interval must be a positive duration such as 1h.", nil)
			return
		}
		schedule, err := app.Imports.SetSchedule(request.Context(), repositoryID, input.Enabled, interval)
		if err != nil {
			writeImportProblem(writer, request, "import schedule change", err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, importScheduleJSON(schedule, true))
	default:
		writeAPIMethodError(writer, http.MethodGet+", "+http.MethodPut)
	}
}

func (app *App) handleImportCredentialsAPI(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	if request.Method == http.MethodDelete && app.clearOrphanImportCredentials(writer, request, repositoryID) {
		return
	}
	if !app.importRepositoryExists(writer, request, repositoryID) {
		return
	}
	switch request.Method {
	case http.MethodPut:
		var input struct {
			Form     string `json:"form"`
			Username string `json:"username"`
			Password string `json:"password"`
			Token    string `json:"token"`
			CAPEM    string `json:"ca_pem"`
		}
		if !decodeAPIJSONLimit(writer, request, &input, MaximumImportCredentialRequest) {
			return
		}
		credential, err := importCredentialFromInput(input.Form, input.Username, input.Password, input.Token, input.CAPEM)
		if err != nil {
			writeAPIError(writer, http.StatusUnprocessableEntity, importsync.CodeInvalidSource, err.Error(), nil)
			return
		}
		if err := app.Imports.SetCredentials(request.Context(), repositoryID, credential); err != nil {
			writeImportProblem(writer, request, "import credential change", err)
			return
		}
		app.writeImportCredentialState(writer, request, repositoryID)
	case http.MethodDelete:
		if err := app.Imports.SetCredentials(request.Context(), repositoryID, nil); err != nil {
			writeImportProblem(writer, request, "import credential change", err)
			return
		}
		app.writeImportCredentialState(writer, request, repositoryID)
	default:
		writeAPIMethodError(writer, http.MethodPut+", "+http.MethodDelete)
	}
}

// clearOrphanImportCredentials handles a clear for a name without a
// repository, such as one left by an interrupted first import: it removes the
// stored source and credentials. It reports false when the repository exists,
// so the ordinary clear applies.
func (app *App) clearOrphanImportCredentials(writer http.ResponseWriter, request *http.Request, repositoryID string) bool {
	_, exists, err := app.Store.Repository(request.Context(), repositoryID)
	if err != nil {
		writeAPIError(writer, unavailable(request, "repository record read", err), importsync.CodeStateUnavailable, "OwnGit state is unavailable.", nil)
		return true
	}
	if exists {
		return false
	}
	forgotten, err := app.Imports.ForgetOrphanImport(request.Context(), repositoryID)
	if importsyncProblemCode(err) == importsync.CodeRepositoryTaken {
		return false
	}
	if err != nil {
		writeImportProblem(writer, request, "orphan import removal", err)
		return true
	}
	if !forgotten {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return true
	}
	writeAPIJSON(writer, http.StatusOK, struct {
		OK              bool   `json:"ok"`
		CredentialForm  string `json:"credential_form"`
		CredentialBound bool   `json:"credential_bound"`
	}{OK: true, CredentialForm: "none", CredentialBound: false})
	return true
}

// writeImportCredentialState reports a committed credential change with the
// stored credential state, which comes from the status read after it.
func (app *App) writeImportCredentialState(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	status, statusErr := app.importStatusAfter(request, repositoryID)
	body := struct {
		OK              bool                          `json:"ok"`
		CredentialForm  *string                       `json:"credential_form"`
		CredentialBound *bool                         `json:"credential_bound"`
		StatusError     *pullrequest.ErrorDescription `json:"status_error,omitempty"`
	}{OK: true, StatusError: statusErr}
	if status != nil {
		body.CredentialForm, body.CredentialBound = &status.CredentialForm, &status.CredentialBound
	}
	writeAPIJSON(writer, http.StatusOK, body)
}

func (app *App) importRepositoryExists(writer http.ResponseWriter, request *http.Request, repositoryID string) bool {
	_, exists, err := app.Store.Repository(request.Context(), repositoryID)
	if err != nil {
		writeAPIError(writer, unavailable(request, "repository record read", err), importsync.CodeStateUnavailable, "OwnGit state is unavailable.", nil)
		return false
	}
	if !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return false
	}
	return true
}

func importCredentialFieldsPresent(form, username, password, token, caPEM string) bool {
	if username != "" || password != "" || token != "" || caPEM != "" {
		return true
	}
	form = strings.TrimSpace(form)
	return form != "" && form != "none"
}

func importCredentialFromInput(form, username, password, token, caPEM string) (*importsync.Credentials, error) {
	form = strings.TrimSpace(form)
	credential := &importsync.Credentials{RootCAPEM: []byte(caPEM)}
	switch form {
	case "basic":
		if token != "" || username == "" || password == "" {
			return nil, errors.New("basic credentials require a username and password and no token")
		}
		credential.Username = username
		credential.Password = password
	case "bearer":
		if username != "" || password != "" || token == "" {
			return nil, errors.New("bearer credentials require a token and no username or password")
		}
		credential.BearerToken = token
	case "", "none":
		if username != "" || password != "" || token != "" {
			return nil, errors.New("credential form is required when a token or password is set")
		}
		if caPEM == "" {
			return nil, errors.New("credential form is required")
		}
	default:
		return nil, errors.New("credential form must be basic, bearer, or none")
	}
	return credential, nil
}

func importSourceJSON(source state.ImportSource) any {
	return struct {
		OK                  bool   `json:"ok"`
		RepositoryID        string `json:"repository_id"`
		URL                 string `json:"url"`
		Mode                string `json:"mode"`
		SourceGeneration    int64  `json:"source_generation"`
		AuthorityRevision   int64  `json:"authority_revision"`
		GitOnlyConsent      bool   `json:"git_only_consent"`
		AllowPrivateNetwork bool   `json:"allow_private_network"`
	}{
		OK: true, RepositoryID: source.RepositoryID, URL: source.URL, Mode: source.Mode,
		SourceGeneration: source.SourceGeneration, AuthorityRevision: source.AuthorityRevision,
		GitOnlyConsent: source.GitOnlyConsent, AllowPrivateNetwork: source.AllowPrivateNetwork,
	}
}

func importScheduleJSON(schedule state.ImportSchedule, exists bool) any {
	return struct {
		OK              bool  `json:"ok"`
		Configured      bool  `json:"configured"`
		Enabled         bool  `json:"enabled"`
		IntervalSeconds int64 `json:"interval_seconds"`
	}{OK: true, Configured: exists, Enabled: schedule.Enabled, IntervalSeconds: schedule.IntervalSeconds}
}

// writeImportProblem answers err, an import problem, as step of request.
func writeImportProblem(writer http.ResponseWriter, request *http.Request, step string, err error) {
	status, code, message, details := importProblemHTTP(request, step, err)
	writeAPIError(writer, status, code, message, details)
}

// importProblemHTTP is the status, code, message and details that answer
// err by its import problem code. An unavailable one is logged as step of
// request. The import service does not classify every failure: its runtime
// preparation, runtime files and state reads can return a plain error, such
// as the context's. Such an error is work that could not be completed now,
// so it is answered as unavailable, logged the same way, and coded
// "unclassified", as importsync records an unclassified run.
func importProblemHTTP(request *http.Request, step string, err error) (int, string, string, any) {
	var problem *importsync.Problem
	if !errors.As(err, &problem) {
		return unavailable(request, step, err), importsync.CodeUnclassified, "import failed", nil
	}
	status := http.StatusBadGateway
	var details any
	switch problem.Code {
	case importsync.CodeRepositoryTaken, importsync.CodeBusy, importsync.CodeSuperseded, importsync.CodeLFSRequired, importsync.CodeUnresolved, importsync.CodeDestinationChanged,
		importsync.CodeNothingToResolve:
		status = http.StatusConflict
	case importsync.CodeNotConfigured:
		status = http.StatusNotFound
	case importsync.CodeRepositoryMissing:
		status = http.StatusNotFound
	case importsync.CodeRuntimeUnavailable, importsync.CodeStateUnavailable, importsync.CodeRuntimeUnsafe:
		status = unavailable(request, step, err)
	case importsync.CodeInvalidSource, importsync.CodeInvalidSchedule, importsync.CodeUnsupportedFormat, importsync.CodeUnsupportedRefs, importsync.CodeUnsupported:
		status = http.StatusUnprocessableEntity
	case importsync.CodeTooLarge, importsync.CodeTooManyRefs:
		status = http.StatusRequestEntityTooLarge
	case importsync.CodeCancelled:
		status = http.StatusOK
	}
	if problem.Code == importsync.CodeLFSRequired {
		details = map[string]any{"git_only_consent_required": true}
	}
	return status, problem.Code, problem.Message, details
}

// MaximumImportCredentialRequest bounds a JSON request that may carry a
// source CA bundle. It admits the full stored CA bound plus the ordinary
// request allowance for the other fields and JSON escaping of line breaks.
const MaximumImportCredentialRequest = int64(state.MaxImportCABytes) + maximumAPIRequest

// importsyncProblemCode is the code of the import problem in err, or
// "unclassified" for an error the import service did not classify.
func importsyncProblemCode(err error) string {
	var problem *importsync.Problem
	if errors.As(err, &problem) {
		return problem.Code
	}
	return importsync.CodeUnclassified
}
