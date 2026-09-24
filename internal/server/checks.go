package server

import (
	"crypto/sha256"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"owngit/internal/auth"
	"owngit/internal/checkapi"
	"owngit/internal/pullrequest"
	"owngit/internal/state"
)

const maximumCheckUpload = checkapi.MaximumUploadBytes

// handleCheckAPI serves evidence transport for the local check helper. Every
// endpoint requires a live repository-scoped helper credential, so helper
// results are authenticated separately from general repository access.
//
// The attempt protocol is two-phase. A registration reserves an immutable
// identity and a server-issued repository-wide sequence before execution, and a
// completion reports the results. Client finish times are evidence, not order.
func (app *App) handleCheckAPI(writer http.ResponseWriter, request *http.Request, repositoryID, resource, remainder string) {
	credential, ok := app.authorizeHelper(writer, request, repositoryID)
	if !ok {
		return
	}
	if _, exists, err := app.Store.Repository(request.Context(), repositoryID); err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	} else if !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return
	}
	if app.refusePreparingAPI(writer, repositoryID) {
		return
	}
	parts := strings.Split(remainder, "/")
	switch {
	case resource == "tasks" && remainder == "":
		switch request.Method {
		case http.MethodGet:
			app.listTasks(writer, request, repositoryID)
		case http.MethodPost:
			app.createTask(writer, request, repositoryID)
		default:
			writeAPIMethodError(writer, http.MethodGet+", "+http.MethodPost)
		}
	case resource == "tasks" && len(parts) == 1 && parts[0] != "":
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		app.showTask(writer, request, repositoryID, parts[0])
	case resource == "tasks" && len(parts) == 2 && parts[1] == "attempts" && parts[0] != "":
		if request.Method != http.MethodPost {
			writeAPIMethodError(writer, http.MethodPost)
			return
		}
		app.registerAttempt(writer, request, repositoryID, parts[0], credential.ID)
	case resource == "tasks" && len(parts) == 4 && parts[1] == "attempts" && parts[3] == "complete" && parts[0] != "" && parts[2] != "":
		if request.Method != http.MethodPost {
			writeAPIMethodError(writer, http.MethodPost)
			return
		}
		app.completeAttempt(writer, request, repositoryID, parts[0], parts[2])
	case resource == "tasks" && len(parts) == 2 && parts[1] == "cycles" && parts[0] != "":
		switch request.Method {
		case http.MethodGet:
			app.listCycles(writer, request, repositoryID, parts[0])
		case http.MethodPost:
			app.reserveCycle(writer, request, repositoryID, parts[0])
		default:
			writeAPIMethodError(writer, http.MethodGet+", "+http.MethodPost)
		}
	case resource == "check-configurations" && remainder == "latest":
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		app.latestCheckConfiguration(writer, request, repositoryID)
	default:
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
	}
}

func (app *App) createTask(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	var input checkapi.CreateTaskInput
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	task, err := app.Store.CreateTask(request.Context(), repositoryID, input.Title, app.now())
	if err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_task", "The task could not be created.", nil)
		return
	}
	writeAPIJSON(writer, http.StatusOK, checkapi.TaskResponse{OK: true, Task: taskJSON(task)})
}

func (app *App) listTasks(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	tasks, err := app.Store.Tasks(request.Context(), repositoryID)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "Task records could not be read.", nil)
		return
	}
	response := checkapi.TaskListResponse{OK: true, Tasks: make([]*checkapi.Task, 0, len(tasks))}
	for _, task := range tasks {
		response.Tasks = append(response.Tasks, taskJSON(task))
	}
	writeAPIJSON(writer, http.StatusOK, response)
}

func (app *App) showTask(writer http.ResponseWriter, request *http.Request, repositoryID, taskID string) {
	task, exists, err := app.Store.Task(request.Context(), repositoryID, taskID)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The task record could not be read.", nil)
		return
	}
	if !exists {
		writeAPIError(writer, http.StatusNotFound, "task_not_found", "The task does not exist.", nil)
		return
	}
	response := checkapi.TaskResponse{OK: true, Task: taskJSON(task)}
	attempt, hasAttempt, err := app.Store.LatestCheckAttemptForTask(request.Context(), repositoryID, taskID)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The latest attempt could not be read.", nil)
		return
	}
	if hasAttempt {
		response.Attempt = attemptJSON(attempt)
	}
	writeAPIJSON(writer, http.StatusOK, response)
}

// registerAttempt reserves the attempt identity and sequence before execution.
func (app *App) registerAttempt(writer http.ResponseWriter, request *http.Request, repositoryID, taskID, credentialID string) {
	var registration checkapi.AttemptRegistration
	if !decodeAPIJSONLimit(writer, request, &registration, maximumCheckUpload) {
		return
	}
	attempt, problem := attemptFromRegistration(registration, repositoryID, taskID, credentialID, app.now())
	if problem != nil {
		writeAPIError(writer, apiStatus(problem.Code), problem.Code, problem.Message, nil)
		return
	}
	task, stored, err := app.Store.RegisterCheckAttempt(request.Context(), attempt)
	if err != nil {
		switch {
		case errors.Is(err, state.ErrTaskNotFound):
			writeAPIError(writer, http.StatusNotFound, "task_not_found", "The task does not exist.", nil)
		case errors.Is(err, state.ErrAttemptConflict):
			writeAPIError(writer, http.StatusConflict, "attempt_conflict", "The attempt identity was reused with different content.", nil)
		case errors.Is(err, state.ErrCycleConflict):
			writeAPIError(writer, http.StatusConflict, "cycle_conflict", "The correction cycle belongs to another task.", nil)
		case errors.Is(err, state.ErrCycleNotFound):
			writeAPIError(writer, http.StatusNotFound, "cycle_not_found", "The correction cycle was not reserved.", nil)
		default:
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The check attempt could not be registered.", nil)
		}
		return
	}
	writeAPIJSON(writer, http.StatusOK, checkapi.TaskResponse{OK: true, Task: taskJSON(task), Attempt: attemptJSON(stored)})
}

// completeAttempt stores the results and the raw log of a registered attempt.
func (app *App) completeAttempt(writer http.ResponseWriter, request *http.Request, repositoryID, taskID, attemptID string) {
	var upload checkapi.AttemptCompletion
	if !decodeAPIJSONLimit(writer, request, &upload, maximumCheckUpload) {
		return
	}
	completion, problem := completionFromUpload(upload, repositoryID, taskID, attemptID)
	if problem != nil {
		writeAPIError(writer, apiStatus(problem.Code), problem.Code, problem.Message, nil)
		return
	}
	task, stored, err := app.Store.CompleteCheckAttempt(request.Context(), completion, app.now())
	if err != nil {
		switch {
		case errors.Is(err, state.ErrTaskNotFound):
			writeAPIError(writer, http.StatusNotFound, "task_not_found", "The task does not exist.", nil)
		case errors.Is(err, state.ErrAttemptNotFound):
			writeAPIError(writer, http.StatusNotFound, "attempt_not_found", "The check attempt was not registered.", nil)
		case errors.Is(err, state.ErrAttemptConflict):
			writeAPIError(writer, http.StatusConflict, "attempt_conflict", "The attempt was already completed with different content.", nil)
		case errors.Is(err, state.ErrResultMismatch):
			writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_attempt", "The results do not match the registered checks.", nil)
		default:
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The check attempt could not be saved.", nil)
		}
		return
	}
	// Keep disposable logs bounded while the server runs for a long time. A
	// prune failure must not fail the upload, but it must not be silent.
	if _, err := app.Store.PruneCheckLogs(request.Context(), app.now()); err != nil {
		log.Printf("could not prune expired check logs: %v", err)
	}
	writeAPIJSON(writer, http.StatusOK, checkapi.TaskResponse{OK: true, Task: taskJSON(task), Attempt: attemptJSON(stored)})
}

func (app *App) reserveCycle(writer http.ResponseWriter, request *http.Request, repositoryID, taskID string) {
	var input checkapi.CreateCycleInput
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	if !validAttemptID(input.CycleID) {
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_cycle_id", "A 32 character lowercase hex cycle identifier is required.", nil)
		return
	}
	task, cycle, err := app.Store.ReserveCorrectionCycle(request.Context(), repositoryID, taskID, input.CycleID, app.now())
	if err != nil {
		switch {
		case errors.Is(err, state.ErrTaskNotFound):
			writeAPIError(writer, http.StatusNotFound, "task_not_found", "The task does not exist.", nil)
		case errors.Is(err, state.ErrCycleConflict):
			writeAPIError(writer, http.StatusConflict, "cycle_conflict", "The correction cycle identity was reused with different content.", nil)
		case errors.Is(err, state.ErrCorrectionBudgetExhausted):
			writeAPIError(writer, http.StatusConflict, "correction_budget_exhausted", "The task has no automatic correction cycle left. A manual check can still be recorded.", nil)
		default:
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The correction cycle could not be reserved.", nil)
		}
		return
	}
	writeAPIJSON(writer, http.StatusOK, checkapi.CycleResponse{OK: true, Task: taskJSON(task), Cycle: cycleJSON(cycle)})
}

func (app *App) listCycles(writer http.ResponseWriter, request *http.Request, repositoryID, taskID string) {
	if _, exists, err := app.Store.Task(request.Context(), repositoryID, taskID); err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The task record could not be read.", nil)
		return
	} else if !exists {
		writeAPIError(writer, http.StatusNotFound, "task_not_found", "The task does not exist.", nil)
		return
	}
	cycles, err := app.Store.CheckCycles(request.Context(), repositoryID, taskID)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "Correction cycles could not be read.", nil)
		return
	}
	response := checkapi.CycleListResponse{OK: true, Cycles: make([]*checkapi.Cycle, 0, len(cycles))}
	for _, cycle := range cycles {
		response.Cycles = append(response.Cycles, cycleJSON(cycle))
	}
	writeAPIJSON(writer, http.StatusOK, response)
}

func (app *App) latestCheckConfiguration(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	configuration, exists, err := app.Store.LatestCheckConfiguration(request.Context(), repositoryID)
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The check configuration could not be read.", nil)
		return
	}
	if !exists {
		writeAPIError(writer, http.StatusNotFound, "configuration_not_found", "No check configuration has been recorded for this repository.", nil)
		return
	}
	writeAPIJSON(writer, http.StatusOK, checkapi.ConfigurationResponse{OK: true, Configuration: configurationJSON(configuration)})
}

// handleCheckAttemptLog serves the disposable raw log of one attempt. Missing
// and expired logs are reported separately because the durable attempt record
// outlives both states.
func (app *App) handleCheckAttemptLog(writer http.ResponseWriter, request *http.Request, repositoryID, remainder string) {
	if _, ok := app.authorizeHelper(writer, request, repositoryID); !ok {
		return
	}
	if app.refusePreparingAPI(writer, repositoryID) {
		return
	}
	parts := strings.Split(remainder, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "log" {
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	}
	if request.Method != http.MethodGet {
		writeAPIMethodError(writer, http.MethodGet)
		return
	}
	attempt, exists, err := app.Store.CheckAttemptByID(request.Context(), repositoryID, parts[0])
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The attempt record could not be read.", nil)
		return
	}
	if !exists {
		writeAPIError(writer, http.StatusNotFound, "attempt_not_found", "The check attempt does not exist.", nil)
		return
	}
	app.writeCheckAttemptLog(writer, attempt)
}

func (app *App) writeCheckAttemptLog(writer http.ResponseWriter, attempt state.CheckAttempt) {
	if attempt.LogID == "" {
		writeAPIError(writer, http.StatusNotFound, "log_not_recorded", "The attempt has no raw log.", nil)
		return
	}
	content, logState, err := app.Store.ReadCheckLog(attempt.LogID, attempt.LogExpiresAt, app.now())
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The raw log could not be read.", nil)
		return
	}
	switch logState {
	case state.CheckLogExpired:
		writeAPIError(writer, http.StatusGone, "log_expired", "The raw log has expired. The durable attempt record remains.", nil)
		return
	case state.CheckLogMissing:
		writeAPIError(writer, http.StatusNotFound, "log_missing", "The raw log is no longer present. The durable attempt record remains.", nil)
		return
	}
	writeAPIJSON(writer, http.StatusOK, checkapi.LogResponse{OK: true, LogID: attempt.LogID, ExpiresAt: attempt.LogExpiresAt, Truncated: attempt.LogTruncated, Content: string(content)})
}

// handleHelperCredentialAPI manages revocable helper credentials. Issuing and
// revoking authority verify the current administrator password, so a helper
// credential can never widen its own authority or change access and security
// settings.
func (app *App) handleHelperCredentialAPI(writer http.ResponseWriter, request *http.Request, repositoryID, remainder string) {
	// Listing may use a browser admin session. A change must verify the
	// current administrator password.
	if !app.authorizeAdminAPI(writer, request, request.Method != http.MethodGet) {
		return
	}
	if _, exists, err := app.Store.Repository(request.Context(), repositoryID); err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "OwnGit state is unavailable.", nil)
		return
	} else if !exists {
		writeAPIError(writer, http.StatusNotFound, "repository_not_found", "The repository does not exist.", nil)
		return
	}
	parts := strings.Split(remainder, "/")
	switch {
	case remainder == "":
		switch request.Method {
		case http.MethodGet:
			credentials, err := app.Store.HelperCredentials(request.Context(), repositoryID)
			if err != nil {
				writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "Helper credentials could not be read.", nil)
				return
			}
			response := checkapi.CredentialListResponse{OK: true, Credentials: make([]*checkapi.Credential, 0, len(credentials))}
			for _, credential := range credentials {
				response.Credentials = append(response.Credentials, credentialJSON(credential))
			}
			writeAPIJSON(writer, http.StatusOK, response)
		case http.MethodPost:
			app.createHelperCredential(writer, request, repositoryID)
		default:
			writeAPIMethodError(writer, http.MethodGet+", "+http.MethodPost)
		}
	case len(parts) == 2 && parts[0] == "by-creation" && parts[1] != "":
		// A scoped compensating revoke after a lost or malformed response.
		// It is idempotent, so it is safe even when nothing was created.
		if request.Method != http.MethodDelete {
			writeAPIMethodError(writer, http.MethodDelete)
			return
		}
		if err := app.Store.RevokeHelperCredentialByCreation(request.Context(), repositoryID, parts[1], app.now()); err != nil {
			writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The helper credential could not be revoked.", nil)
			return
		}
		writeAPIJSON(writer, http.StatusOK, checkapi.OKResponse{OK: true})
	case len(parts) == 1 && parts[0] != "":
		if request.Method != http.MethodDelete {
			writeAPIMethodError(writer, http.MethodDelete)
			return
		}
		if err := app.Store.RevokeHelperCredential(request.Context(), repositoryID, parts[0], app.now()); err != nil {
			writeAPIError(writer, http.StatusConflict, "credential_not_found", "The helper credential was not found or was already revoked.", nil)
			return
		}
		writeAPIJSON(writer, http.StatusOK, checkapi.OKResponse{OK: true})
	default:
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
	}
}

func (app *App) createHelperCredential(writer http.ResponseWriter, request *http.Request, repositoryID string) {
	var input checkapi.CreateCredentialInput
	if !decodeAPIJSON(writer, request, &input) {
		return
	}
	if input.CreationID != "" && !validAttemptID(input.CreationID) {
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_credential", "A 32 character lowercase hex creation identifier is required.", nil)
		return
	}
	credential, token, created, err := app.issueHelperCredential(request.Context(), repositoryID, input.Label, input.CreationID)
	if err != nil {
		if errors.Is(err, state.ErrCreationConflict) {
			writeAPIError(writer, http.StatusConflict, "creation_conflict", "The creation identity was already used with a different label.", nil)
			return
		}
		writeAPIError(writer, http.StatusUnprocessableEntity, "invalid_credential", "The helper credential could not be created.", nil)
		return
	}
	response := checkapi.CredentialResponse{OK: true, Credential: credentialJSON(credential)}
	if created {
		response.Token = token
	}
	writeAPIJSON(writer, http.StatusOK, response)
}

func (app *App) authorizeHelper(writer http.ResponseWriter, request *http.Request, repositoryID string) (state.HelperCredential, bool) {
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="OwnGit"`)
		writeAPIError(writer, http.StatusUnauthorized, "helper_authentication_required", "A repository-scoped helper credential is required.", nil)
		return state.HelperCredential{}, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if token == "" {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="OwnGit"`)
		writeAPIError(writer, http.StatusUnauthorized, "helper_authentication_required", "A repository-scoped helper credential is required.", nil)
		return state.HelperCredential{}, false
	}
	hash := sha256.Sum256([]byte(token))
	credential, ok, err := app.Store.HelperCredentialByToken(request.Context(), hash[:], app.now())
	if err != nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "state_unavailable", "The helper credential could not be verified.", nil)
		return state.HelperCredential{}, false
	}
	if !ok {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="OwnGit"`)
		writeAPIError(writer, http.StatusUnauthorized, "invalid_helper_credential", "The helper credential is unknown or revoked.", nil)
		return state.HelperCredential{}, false
	}
	if credential.RepositoryID != repositoryID {
		writeAPIError(writer, http.StatusForbidden, "helper_credential_scope", "The helper credential belongs to another repository.", nil)
		return state.HelperCredential{}, false
	}
	return credential, true
}

// csrfHeader carries the admin session CSRF token for browser API calls.
const csrfHeader = "X-Owngit-CSRF"

// adminPasswordHeader carries the current administrator password for a browser
// security change, so the dashboard does not have to build a Basic header.
const adminPasswordHeader = "X-Owngit-Admin-Password"

// authorizeAdminAPI authenticates an administrator API request.
//
// A security change must verify the current administrator password, because a
// remembered browser session must not silently issue or revoke authority. The
// password travels in the Basic header (CLI) or in the admin password header
// (browser). A read may instead use a browser admin session with its CSRF
// token, which follows the existing browser rules.
func (app *App) authorizeAdminAPI(writer http.ResponseWriter, request *http.Request, requirePassword bool) bool {
	username, password, hasBasic := request.BasicAuth()
	if hasBasic {
		if username != "admin" {
			writer.Header().Set("WWW-Authenticate", `Basic realm="OwnGit admin"`)
			writeAPIError(writer, http.StatusUnauthorized, "admin_authentication_required", "The administrator password is required.", nil)
			return false
		}
		return app.verifyAdminPassword(writer, request, password)
	}

	if _, ok := app.cookieSession(request, "admin", adminCookie); !ok {
		writer.Header().Set("WWW-Authenticate", `Basic realm="OwnGit admin"`)
		writeAPIError(writer, http.StatusUnauthorized, "admin_authentication_required", "The administrator password is required.", nil)
		return false
	}
	if !app.validCSRF(request, request.Header.Get(csrfHeader)) {
		writeAPIError(writer, http.StatusForbidden, "csrf_required", "The "+csrfHeader+" header does not match the admin session.", nil)
		return false
	}
	password = request.Header.Get(adminPasswordHeader)
	if requirePassword && password == "" {
		writer.Header().Set("WWW-Authenticate", `Basic realm="OwnGit admin"`)
		writeAPIError(writer, http.StatusUnauthorized, "admin_password_required", "The current administrator password is required to change protected settings.", nil)
		return false
	}
	if password != "" {
		return app.verifyAdminPassword(writer, request, password)
	}
	return true
}

func (app *App) verifyAdminPassword(writer http.ResponseWriter, request *http.Request, password string) bool {
	if err := app.Auth.VerifyCredential(request.Context(), "admin", password, request.RemoteAddr); err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			writeAPIError(writer, http.StatusTooManyRequests, "authentication_rate_limited", "Too many authentication attempts. Try again later.", nil)
			return false
		}
		writer.Header().Set("WWW-Authenticate", `Basic realm="OwnGit admin"`)
		writeAPIError(writer, http.StatusUnauthorized, "invalid_admin_credentials", "The administrator password is invalid.", nil)
		return false
	}
	return true
}

func attemptFromRegistration(registration checkapi.AttemptRegistration, repositoryID, taskID, credentialID string, now time.Time) (state.CheckAttempt, *pullrequest.Problem) {
	if !validAttemptID(registration.AttemptID) {
		return state.CheckAttempt{}, pullrequest.NewProblem("invalid_attempt_id", "A 32 character lowercase hex attempt identifier is required.")
	}
	if !validOID(registration.RevisionOID) {
		return state.CheckAttempt{}, pullrequest.NewProblem("invalid_revision_oid", "A lowercase 40 or 64 character revision object ID is required.")
	}
	if registration.TimeoutMS < 0 || registration.OutputLimitBytes < 0 {
		return state.CheckAttempt{}, pullrequest.NewProblem("invalid_attempt", "The execution limits must not be negative.")
	}
	if registration.CycleID != "" && !validAttemptID(registration.CycleID) {
		return state.CheckAttempt{}, pullrequest.NewProblem("invalid_cycle_id", "A 32 character lowercase hex cycle identifier is required.")
	}
	if registration.JobID != "" && !validAttemptID(registration.JobID) {
		return state.CheckAttempt{}, pullrequest.NewProblem("invalid_job_id", "A 32 character lowercase hex job identifier is required.")
	}
	switch registration.WorktreeState {
	case state.WorktreeClean, state.WorktreeDirty, state.WorktreeUnknown:
	default:
		return state.CheckAttempt{}, pullrequest.NewProblem("invalid_worktree_state", "The worktree state must be clean, dirty, or unknown.")
	}
	if len(registration.Checks) > state.MaximumCheckDefinitions {
		return state.CheckAttempt{}, pullrequest.NewProblem("invalid_check_definition", "At most 50 check definitions are allowed.")
	}
	checks := make([]state.CheckDefinition, 0, len(registration.Checks))
	for _, check := range registration.Checks {
		if !validCheckText(check.Name, state.MaximumCheckNameBytes) || !validCheckText(check.Command, state.MaximumCheckCommandBytes) {
			return state.CheckAttempt{}, pullrequest.NewProblem("invalid_check_definition", "Each check needs a name and a command.")
		}
		checks = append(checks, state.CheckDefinition{Name: check.Name, Command: check.Command})
	}
	if registration.StartedAt.IsZero() {
		return state.CheckAttempt{}, pullrequest.NewProblem("invalid_attempt", "The attempt needs a valid start time.")
	}
	return state.CheckAttempt{
		ID: registration.AttemptID, TaskID: taskID, RepositoryID: repositoryID, RevisionOID: registration.RevisionOID,
		WorktreeState: registration.WorktreeState, StartedAt: registration.StartedAt.UTC(), CycleID: registration.CycleID,
		JobID:      registration.JobID,
		Protection: state.ProtectionUnknown, ExecutionScope: state.ExecutionScopeInherited, CredentialID: credentialID,
		TimeoutMS: registration.TimeoutMS, OutputLimitBytes: registration.OutputLimitBytes, CreatedAt: now, Checks: checks,
	}, nil
}

func completionFromUpload(upload checkapi.AttemptCompletion, repositoryID, taskID, attemptID string) (state.CheckCompletion, *pullrequest.Problem) {
	if upload.FinishedAt.IsZero() {
		return state.CheckCompletion{}, pullrequest.NewProblem("invalid_attempt", "The attempt needs a valid finish time.")
	}
	switch upload.WorktreeState {
	case state.WorktreeClean, state.WorktreeDirty, state.WorktreeUnknown:
	default:
		return state.CheckCompletion{}, pullrequest.NewProblem("invalid_worktree_state", "The worktree state must be clean, dirty, or unknown.")
	}
	if len(upload.Results) > state.MaximumCheckDefinitions {
		return state.CheckCompletion{}, pullrequest.NewProblem("invalid_attempt", "At most 50 check results are allowed.")
	}
	// Over-long text is clipped instead of refused. The completion is otherwise
	// finished evidence, and refusing it would lose the result. The stored bounds
	// stay the limit, the truncation flags record the cut, and the request body
	// itself remains bounded by checkapi.MaximumUploadBytes.
	rawLog, logCut := checkapi.ClipText(upload.Log, state.MaximumCheckLogBytes)
	results := make([]state.CheckResult, 0, len(upload.Results))
	for _, result := range upload.Results {
		if !validCheckText(result.Name, state.MaximumCheckNameBytes) || !validCheckText(result.Command, state.MaximumCheckCommandBytes) {
			return state.CheckCompletion{}, pullrequest.NewProblem("invalid_attempt", "Each result needs a name and a command.")
		}
		if !validResultStatus(result.Status) {
			return state.CheckCompletion{}, pullrequest.NewProblem("invalid_attempt", "A check result has an unknown status.")
		}
		if result.DurationMS < 0 {
			return state.CheckCompletion{}, pullrequest.NewProblem("invalid_attempt", "A check result has an invalid duration or excerpt.")
		}
		excerpt, excerptCut := checkapi.ClipText(result.OutputExcerpt, state.MaximumCheckExcerptBytes)
		if len(result.CleanupError) > state.MaximumCleanupErrorBytes {
			return state.CheckCompletion{}, pullrequest.NewProblem("invalid_attempt", "A check result has an oversized cleanup error.")
		}
		results = append(results, state.CheckResult{
			Name: result.Name, Command: result.Command, Status: result.Status, ExitCode: result.ExitCode,
			DurationMS: result.DurationMS, OutputExcerpt: excerpt, Truncated: result.Truncated || excerptCut,
			CleanupError: result.CleanupError,
		})
	}
	return state.CheckCompletion{
		AttemptID: attemptID, RepositoryID: repositoryID, TaskID: taskID, Results: results, Cancelled: upload.Cancelled,
		FinishedAt: upload.FinishedAt.UTC(), WorktreeState: upload.WorktreeState, Log: rawLog, LogTruncated: upload.LogTruncated || logCut,
	}, nil
}

func validResultStatus(status string) bool {
	switch status {
	case state.AttemptPassed, state.AttemptFailed, state.AttemptError, state.AttemptCancelled, state.AttemptIncomplete, state.AttemptUnavailable:
		return true
	default:
		return false
	}
}

func validCheckText(value string, maximum int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len(value) <= maximum && !strings.ContainsAny(value, "\x00\r\n")
}

func validAttemptID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func taskJSON(task state.Task) *checkapi.Task {
	return &checkapi.Task{
		ID: task.ID, RepositoryID: task.RepositoryID, Title: task.Title, Status: task.Status,
		CorrectionCyclesUsed: task.CorrectionCyclesUsed, CorrectionCyclesRemaining: task.CorrectionCyclesRemaining(),
		CorrectionCycleLimit: state.CorrectionCycleLimit, InitialCheckDone: task.InitialCheckDone,
		LastRegisteredSequence: task.LastRegisteredSequence, LastAppliedSequence: task.LastAppliedSequence,
		PendingAttemptID: task.PendingAttemptID, LastAppliedAttemptID: task.LastAppliedAttemptID,
		CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
	}
}

func attemptJSON(attempt state.CheckAttempt) *checkapi.Attempt {
	response := &checkapi.Attempt{
		ID: attempt.ID, TaskID: attempt.TaskID, RepositoryID: attempt.RepositoryID, RevisionOID: attempt.RevisionOID,
		WorktreeState: attempt.EffectiveWorktreeState(), ConfigurationVersion: attempt.ConfigurationVersion, Status: attempt.Status,
		ExitCode: attempt.ExitCode, StartedAt: attempt.StartedAt, FinishedAt: attempt.FinishedAt, DurationMS: attempt.DurationMS,
		Summary: attempt.Summary, Sequence: attempt.Sequence, CycleID: attempt.CycleID, JobID: attempt.JobID,
		Protection: attempt.Protection, ExecutionScope: attempt.ExecutionScope,
		CredentialID: attempt.CredentialID, TimeoutMS: attempt.TimeoutMS, OutputLimitBytes: attempt.OutputLimitBytes,
		LogID: attempt.LogID, LogExpiresAt: attempt.LogExpiresAt, LogTruncated: attempt.LogTruncated, LogError: attempt.LogError,
		CleanupFailed: attempt.CleanupFailed(),
	}
	for _, result := range attempt.Results {
		response.Results = append(response.Results, checkapi.Result{
			Name: result.Name, Command: result.Command, Status: result.Status, ExitCode: result.ExitCode,
			DurationMS: result.DurationMS, OutputExcerpt: result.OutputExcerpt, Truncated: result.Truncated,
			CleanupError: result.CleanupError,
		})
	}
	return response
}

func cycleJSON(cycle state.CheckCycle) *checkapi.Cycle {
	return &checkapi.Cycle{ID: cycle.ID, TaskID: cycle.TaskID, Sequence: cycle.Sequence, ReservedAt: cycle.ReservedAt, AttemptID: cycle.AttemptID}
}

func configurationJSON(configuration state.CheckConfiguration) *checkapi.Configuration {
	response := &checkapi.Configuration{Version: configuration.Version, CreatedAt: configuration.CreatedAt}
	for _, check := range configuration.Checks {
		response.Checks = append(response.Checks, checkapi.CheckDefinition{Name: check.Name, Command: check.Command})
	}
	return response
}

func credentialJSON(credential state.HelperCredential) *checkapi.Credential {
	return &checkapi.Credential{
		ID: credential.ID, RepositoryID: credential.RepositoryID, Label: credential.Label,
		CreationID: credential.CreationID, CreatedAt: credential.CreatedAt, RevokedAt: credential.RevokedAt, LastUsedAt: credential.LastUsedAt,
	}
}
