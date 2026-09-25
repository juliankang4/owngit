package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"owngit/internal/auth"
	"owngit/internal/pullrequest"
	"owngit/internal/requestctx"
	"owngit/internal/state"
)

const (
	maximumAPIRequest  = 64 << 10
	maximumAPIResponse = 4 << 20
)

func (app *App) handleAPI(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	if !strings.HasPrefix(request.URL.Path, "/api/v1/") {
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	}
	if !settings.Initialized {
		writeAPIError(writer, http.StatusConflict, "setup_incomplete", "OwnGit setup is not complete.", nil)
		return
	}
	repositoryID, resource, remainder, repositoryRoute := parseRepositoryAPIRoute(request.URL.Path)
	if request.URL.RawQuery != "" && !importHistoryQueryAllowed(request, repositoryRoute, resource, remainder) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request", "This API endpoint does not accept query parameters.", nil)
		return
	}
	if id, ok := repositoryAPIRoute(request.URL.Path); ok {
		app.handleRepositoryAPI(writer, request, settings, id)
		return
	}
	if repositoryRoute {
		switch resource {
		case "runner":
			app.handleRunnerAPI(writer, request, repositoryID, remainder)
			return
		case "check-policy", "check-jobs", "runner-credentials":
			app.handleConfiguredCheckOwnerAPI(writer, request, repositoryID, resource, remainder)
			return
		case "tasks", "check-configurations":
			app.handleCheckAPI(writer, request, repositoryID, resource, remainder)
			return
		case "check-attempts":
			app.handleCheckAttemptLog(writer, request, repositoryID, remainder)
			return
		case "helper-credentials":
			app.handleHelperCredentialAPI(writer, request, repositoryID, remainder)
			return
		case "import":
			app.handleImportAPI(writer, request, repositoryID, remainder)
			return
		}
	}
	if app.PullRequests == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "service_unavailable", "The pull request service is unavailable.", nil)
		return
	}
	if !app.authorizeAPI(writer, request, settings) {
		return
	}

	repositoryID, number, operation, ok := parsePullRequestAPIRoute(request.URL.Path)
	if !ok {
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	}
	if operation == "collection" {
		switch request.Method {
		case http.MethodGet:
			operation = "list"
		case http.MethodPost:
			operation = "create"
		default:
			writeAPIMethodError(writer, http.MethodGet+", "+http.MethodPost)
			return
		}
	}

	var result any
	var err error
	switch operation {
	case "list":
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		var items []*pullrequest.View
		items, err = app.PullRequests.List(request.Context(), repositoryID)
		if err == nil {
			result = struct {
				OK    bool                `json:"ok"`
				Items []*pullrequest.View `json:"pull_requests"`
			}{OK: true, Items: items}
		}
	case "create":
		if request.Method != http.MethodPost {
			writeAPIMethodError(writer, http.MethodPost)
			return
		}
		var input pullrequest.CreateInput
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		if input.Repository != "" && input.Repository != repositoryID {
			writeAPIError(writer, http.StatusBadRequest, "invalid_repository", "The body repository does not match the API path.", nil)
			return
		}
		input.Repository = repositoryID
		var view *pullrequest.View
		view, err = app.PullRequests.Create(request.Context(), input)
		if err == nil {
			result = pullrequest.SuccessEnvelope{OK: true, PullRequest: view}
		}
	case "show":
		if request.Method != http.MethodGet {
			writeAPIMethodError(writer, http.MethodGet)
			return
		}
		var view *pullrequest.View
		view, err = app.PullRequests.Show(request.Context(), repositoryID, number)
		if err == nil {
			result = pullrequest.SuccessEnvelope{OK: true, PullRequest: view}
		}
	case "review_request", "review_skip", "merge":
		if request.Method != http.MethodPost {
			writeAPIMethodError(writer, http.MethodPost)
			return
		}
		var input pullrequest.RevisionInput
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		var view *pullrequest.View
		switch operation {
		case "review_request":
			view, err = app.PullRequests.RequestReview(request.Context(), repositoryID, number, input)
		case "review_skip":
			view, err = app.PullRequests.SkipReview(request.Context(), repositoryID, number, input)
		case "merge":
			view, err = app.PullRequests.Merge(request.Context(), repositoryID, number, input)
		}
		if err == nil {
			result = pullrequest.SuccessEnvelope{OK: true, PullRequest: view}
		}
	case "close", "reopen":
		if request.Method != http.MethodPost {
			writeAPIMethodError(writer, http.MethodPost)
			return
		}
		// The body is an empty JSON object. Requiring JSON keeps the same
		// cross-site protection as every other mutating call.
		var input struct{}
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		var view *pullrequest.View
		if operation == "close" {
			view, err = app.PullRequests.Close(request.Context(), repositoryID, number)
		} else {
			view, err = app.PullRequests.Reopen(request.Context(), repositoryID, number)
		}
		if err == nil {
			result = pullrequest.SuccessEnvelope{OK: true, PullRequest: view}
		}
	case "review_submit":
		if request.Method != http.MethodPost {
			writeAPIMethodError(writer, http.MethodPost)
			return
		}
		var input pullrequest.ReviewSubmitInput
		if !decodeAPIJSON(writer, request, &input) {
			return
		}
		var view *pullrequest.View
		view, err = app.PullRequests.SubmitReview(request.Context(), repositoryID, number, input)
		if err == nil {
			result = pullrequest.SuccessEnvelope{OK: true, PullRequest: view}
		}
	default:
		writeAPIError(writer, http.StatusNotFound, "not_found", "The API endpoint does not exist.", nil)
		return
	}
	if err != nil {
		problem := pullrequest.AsProblem(err)
		writeAPIError(writer, apiStatus(problem.Code), problem.Code, problem.Message, problem.Details)
		return
	}
	writeAPIJSON(writer, http.StatusOK, result)
}

func (app *App) authorizeAPI(writer http.ResponseWriter, request *http.Request, settings state.Settings) bool {
	if settings.AccessMode == "open" {
		return true
	}
	_, password, ok := request.BasicAuth()
	if !ok {
		writer.Header().Set("WWW-Authenticate", `Basic realm="OwnGit"`)
		writeAPIError(writer, http.StatusUnauthorized, "authentication_required", "A shared general-access password is required.", nil)
		return false
	}
	if err := app.Auth.VerifyCredential(request.Context(), "general", password, requestctx.Of(request).ClientAddress); err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			writeAPIError(writer, http.StatusTooManyRequests, "authentication_rate_limited", "Too many authentication attempts. Try again later.", nil)
			return false
		}
		writer.Header().Set("WWW-Authenticate", `Basic realm="OwnGit"`)
		writeAPIError(writer, http.StatusUnauthorized, "invalid_credentials", "The shared general-access password is invalid.", nil)
		return false
	}
	return true
}

func parseRepositoryAPIRoute(requestPath string) (string, string, string, bool) {
	const prefix = "/api/v1/repositories/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(requestPath, prefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	resource, remainder := parts[1], ""
	if index := strings.Index(parts[1], "/"); index >= 0 {
		resource, remainder = parts[1][:index], parts[1][index+1:]
	}
	return parts[0], resource, remainder, true
}

func parsePullRequestAPIRoute(requestPath string) (string, int64, string, bool) {
	const prefix = "/api/v1/repositories/"
	if !strings.HasPrefix(requestPath, prefix) {
		return "", 0, "", false
	}
	parts := strings.Split(strings.TrimPrefix(requestPath, prefix), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] != "pull-requests" {
		return "", 0, "", false
	}
	if len(parts) == 2 {
		return parts[0], 0, "collection", true
	}
	number, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || number <= 0 {
		return "", 0, "", false
	}
	if len(parts) == 3 {
		return parts[0], number, "show", true
	}
	if len(parts) == 4 && (parts[3] == "merge" || parts[3] == "close" || parts[3] == "reopen") {
		return parts[0], number, parts[3], true
	}
	if len(parts) == 5 && parts[3] == "review" {
		switch parts[4] {
		case "request":
			return parts[0], number, "review_request", true
		case "submit":
			return parts[0], number, "review_submit", true
		case "skip":
			return parts[0], number, "review_skip", true
		}
	}
	return "", 0, "", false
}

func decodeAPIJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	return decodeAPIJSONLimit(writer, request, destination, maximumAPIRequest)
}

func decodeAPIJSONLimit(writer http.ResponseWriter, request *http.Request, destination any, limit int64) bool {
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(parameters) != 0 {
		writeAPIError(writer, http.StatusUnsupportedMediaType, "json_required", "Mutating API requests require Content-Type application/json.", nil)
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeAPIError(writer, http.StatusRequestEntityTooLarge, "request_too_large", "The JSON request exceeds the supported size.", nil)
		} else {
			writeAPIError(writer, http.StatusBadRequest, "invalid_json", "The request body must contain one valid JSON object with known fields.", nil)
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeAPIError(writer, http.StatusBadRequest, "invalid_json", "The request body must contain exactly one JSON object.", nil)
		return false
	}
	return true
}

// refusePreparingAPI answers a request for a repository that is still being
// prepared after startup with a fixed 503 and reports whether it did. Call it
// after authorization, so the answer does not reveal the repository to an
// unauthenticated caller.
func (app *App) refusePreparingAPI(writer http.ResponseWriter, repositoryID string) bool {
	if app.Repositories == nil || !app.Repositories.Preparing(repositoryID) {
		return false
	}
	writer.Header().Set("Retry-After", "30")
	writeAPIError(writer, http.StatusServiceUnavailable, "repository_preparing", "The repository is being prepared. Try again later.", nil)
	return true
}

func writeAPIMethodError(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writeAPIError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "The HTTP method is not allowed for this API endpoint.", nil)
}

func writeAPIError(writer http.ResponseWriter, status int, code, message string, details any) {
	description := pullrequest.ErrorDescription{Code: code, Message: message}
	if details != nil {
		if encoded, err := json.Marshal(details); err == nil {
			description.Details = encoded
		}
	}
	writeAPIJSON(writer, status, pullrequest.ErrorEnvelope{OK: false, Error: description})
}

func writeAPIJSON(writer http.ResponseWriter, status int, value any) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil || output.Len() > maximumAPIResponse {
		output.Reset()
		_ = json.NewEncoder(&output).Encode(pullrequest.ErrorEnvelope{
			OK: false, Error: pullrequest.ErrorDescription{Code: "response_too_large", Message: "The API response exceeds the supported size."},
		})
		status = http.StatusInsufficientStorage
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write(output.Bytes())
}

func apiStatus(code string) int {
	switch code {
	case "invalid_repository", "invalid_pull_request_number", "invalid_title", "invalid_branch", "reserved_ref", "same_branch", "invalid_review_choice", "invalid_review_decision", "invalid_reviewer_label", "invalid_revision",
		"invalid_task", "invalid_credential", "invalid_attempt", "invalid_attempt_id", "invalid_check_definition", "invalid_worktree_state", "invalid_revision_oid", "invalid_cycle_id":
		return http.StatusUnprocessableEntity
	case "repository_not_found", "pull_request_not_found", "task_not_found", "configuration_not_found", "attempt_not_found", "log_not_recorded", "cycle_not_found":
		return http.StatusNotFound
	case "stale_revision", "merge_conflict", "merge_blocked", "pull_request_not_open", "pull_request_exists", "pull_request_merged", "git_update_failed",
		"source_branch_missing", "target_branch_missing", "source_not_commit", "target_not_commit", "credential_not_found", "attempt_conflict", "cycle_conflict", "correction_budget_exhausted":
		return http.StatusConflict
	case "helper_authentication_required", "invalid_helper_credential", "admin_authentication_required", "invalid_admin_credentials", "admin_password_required":
		return http.StatusUnauthorized
	case "helper_credential_scope", "csrf_required":
		return http.StatusForbidden
	case "log_expired":
		return http.StatusGone
	case "unsupported_git":
		return http.StatusNotImplemented
	case "result_too_large":
		return http.StatusRequestEntityTooLarge
	case "state_unavailable", "repository_unavailable", "repository_preparing", "repository_busy", "merge_reconciliation_pending", "pull_request_creation_reconciliation_pending":
		return http.StatusServiceUnavailable
	case "repository_integrity_error":
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
