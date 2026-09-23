package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"owngit/internal/importsync"
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
	if app.Imports == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, importsync.CodeRuntimeUnavailable, "The import service is unavailable.", nil)
		return
	}
	if !app.authorizeAdminAPI(writer, request, request.Method != http.MethodGet) {
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
			writeImportProblem(writer, err)
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
			writeImportProblem(writer, err)
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
	_, _, exists, err := app.Repositories.ExistingPath(request.Context(), repositoryID)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, importsync.CodeStateUnavailable, "The repository destination could not be read.", nil)
		return
	}
	if exists {
		if importCredentialFieldsPresent(input.CredentialForm, input.Username, input.Password, input.Token, input.CAPEM) {
			writeAPIError(writer, http.StatusUnprocessableEntity, importsync.CodeInvalidSource, "Refresh does not accept credentials. Save them with the credentials endpoint first.", nil)
			return
		}
		run, runErr := app.Imports.Refresh(request.Context(), repositoryID, app.importRunLimits())
		app.writeImportRunResult(writer, request, repositoryID, run, runErr)
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
	if importCredentialFieldsPresent(input.CredentialForm, input.Username, input.Password, input.Token, input.CAPEM) {
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

func (app *App) writeImportRunResult(writer http.ResponseWriter, request *http.Request, repositoryID string, run state.ImportRun, runErr error) {
	status, statusErr := app.Imports.Status(request.Context(), repositoryID)
	if statusErr != nil && runErr == nil {
		runErr = statusErr
	}
	body := struct {
		OK           bool               `json:"ok"`
		Code         string             `json:"code,omitempty"`
		RepositoryID string             `json:"repository_id,omitempty"`
		Run          importsync.RunView `json:"run"`
		Status       importsync.Status  `json:"status"`
	}{RepositoryID: repositoryID, Run: importsync.RunRecordView(run), Status: status}
	if runErr == nil {
		body.OK = true
		writeAPIJSON(writer, http.StatusOK, body)
		return
	}
	code := importsyncProblemCode(runErr)
	if code == importsync.CodeCancelled {
		body.OK = true
		body.Code = code
		writeAPIJSON(writer, http.StatusOK, body)
		return
	}
	writeImportProblem(writer, runErr)
}

func (app *App) handleImportCancelAPI(writer http.ResponseWriter, request *http.Request, repositoryID string) {
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
	cancelled, err := app.Imports.Cancel(request.Context(), repositoryID)
	if err != nil {
		writeImportProblem(writer, err)
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
		writeImportProblem(writer, err)
		return
	}
	writeAPIJSON(writer, http.StatusOK, struct {
		OK       bool              `json:"ok"`
		Resolved []string          `json:"resolved"`
		Status   importsync.Status `json:"status"`
	}{OK: true, Resolved: result.Resolved, Status: result.Status})
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
		writeImportProblem(writer, err)
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
			writeImportProblem(writer, err)
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
			writeAPIError(writer, http.StatusUnprocessableEntity, importsync.CodeInvalidSource, "Schedule interval must be a positive duration such as 1h.", nil)
			return
		}
		schedule, err := app.Imports.SetSchedule(request.Context(), repositoryID, input.Enabled, interval)
		if err != nil {
			writeImportProblem(writer, err)
			return
		}
		writeAPIJSON(writer, http.StatusOK, importScheduleJSON(schedule, true))
	default:
		writeAPIMethodError(writer, http.MethodGet+", "+http.MethodPut)
	}
}

func (app *App) handleImportCredentialsAPI(writer http.ResponseWriter, request *http.Request, repositoryID string) {
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
			writeImportProblem(writer, err)
			return
		}
		app.writeImportCredentialState(writer, request, repositoryID)
	case http.MethodDelete:
		if err := app.Imports.SetCredentials(request.Context(), repositoryID, nil); err != nil {
			writeImportProblem(writer, err)
			return
		}
		app.writeImportCredentialState(writer, request, repositoryID)
	default:
		writeAPIMethodError(writer, http.MethodPut+", "+http.MethodDelete)
	}
}

func (app *App) writeImportCredentialState(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	status, err := app.Imports.Status(request.Context(), repositoryID)
	if err != nil {
		writeImportProblem(writer, err)
		return
	}
	writeAPIJSON(writer, http.StatusOK, struct {
		OK              bool   `json:"ok"`
		CredentialForm  string `json:"credential_form"`
		CredentialBound bool   `json:"credential_bound"`
	}{OK: true, CredentialForm: status.CredentialForm, CredentialBound: status.CredentialBound})
}

func (app *App) importRepositoryExists(writer http.ResponseWriter, request *http.Request, repositoryID string) bool {
	_, exists, err := app.Store.Repository(request.Context(), repositoryID)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, importsync.CodeStateUnavailable, "OwnGit state is unavailable.", nil)
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

func writeImportProblem(writer http.ResponseWriter, err error) {
	status, code, message, details := importProblemHTTP(err)
	writeAPIError(writer, status, code, message, details)
}

func importProblemHTTP(err error) (int, string, string, any) {
	var problem *importsync.Problem
	if !errors.As(err, &problem) {
		return http.StatusInternalServerError, importsync.CodeUnsupported, "import failed", nil
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
		status = http.StatusServiceUnavailable
	case importsync.CodeInvalidSource, importsync.CodeUnsupportedFormat, importsync.CodeUnsupportedRefs, importsync.CodeUnsupported:
		status = http.StatusUnprocessableEntity
	case importsync.CodeTooLarge:
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

func importsyncProblemCode(err error) string {
	var problem *importsync.Problem
	if errors.As(err, &problem) {
		return problem.Code
	}
	return importsync.CodeUnsupported
}
