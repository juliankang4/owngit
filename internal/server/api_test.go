package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/pullrequest"
	"owngit/internal/state"
	"owngit/internal/testfixture"
)

func TestPullRequestAPIJourneyUsesJSONAndExactRevisions(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server := serve(t, fixture.app.Handler())
	endpoint := server.URL + "/api/v1/repositories/project/pull-requests"

	foreign := apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Feature", "source_branch": "feature", "target_branch": "main", "review": "request",
	}, "", "https://foreign.example")
	if foreign.StatusCode != http.StatusForbidden || apiErrorCode(t, foreign) != "origin_mismatch" {
		t.Fatalf("foreign Origin status=%d", foreign.StatusCode)
	}
	if foreign.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("API enabled CORS for a foreign Origin")
	}

	formRequest, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader("title=Feature"))
	noErr(t, err)
	formRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	formResponse, err := http.DefaultClient.Do(formRequest)
	noErr(t, err)
	if formResponse.StatusCode != http.StatusUnsupportedMediaType || apiErrorCode(t, formResponse) != "json_required" {
		t.Fatalf("form mutation status=%d", formResponse.StatusCode)
	}

	oversized := apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": strings.Repeat("x", maximumAPIRequest), "source_branch": "feature", "target_branch": "main", "review": "request",
	}, "", "")
	if oversized.StatusCode != http.StatusRequestEntityTooLarge || apiErrorCode(t, oversized) != "request_too_large" {
		t.Fatalf("oversized mutation status=%d", oversized.StatusCode)
	}

	createdResponse := apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Feature", "source_branch": "feature", "target_branch": "main", "review": "request",
	}, "", "")
	if createdResponse.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d error=%q", createdResponse.StatusCode, apiErrorCode(t, createdResponse))
	}
	created := decodeAPISuccess(t, createdResponse)
	if created.PullRequest == nil || created.PullRequest.Review.Status != state.ReviewPending || created.PullRequest.Checks.Status != "absent" || !created.PullRequest.Checks.Advisory {
		t.Fatalf("create response=%+v", created.PullRequest)
	}
	number := created.PullRequest.Number

	staleResponse := apiRequest(t, http.MethodPost, endpoint+"/"+itoa(number)+"/review/skip", pullrequest.RevisionInput{
		SourceOID: fixture.targetOID, TargetOID: fixture.targetOID,
	}, "", "")
	if staleResponse.StatusCode != http.StatusConflict || apiErrorCode(t, staleResponse) != "stale_revision" {
		t.Fatalf("stale review status=%d", staleResponse.StatusCode)
	}

	reviewResponse := apiRequest(t, http.MethodPost, endpoint+"/"+itoa(number)+"/review/submit", pullrequest.ReviewSubmitInput{
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID, Decision: state.ReviewApproved, ReviewerLabel: "existing-tool: api-test",
	}, "", "")
	if reviewResponse.StatusCode != http.StatusOK {
		t.Fatalf("review status=%d error=%q", reviewResponse.StatusCode, apiErrorCode(t, reviewResponse))
	}
	reviewed := decodeAPISuccess(t, reviewResponse)
	if reviewed.PullRequest == nil || !reviewed.PullRequest.MergeEligibility.Eligible || reviewed.PullRequest.Review.Independent || reviewed.PullRequest.Review.ExecutedChecks {
		t.Fatalf("review response=%+v", reviewed.PullRequest)
	}

	mergeResponse := apiRequest(t, http.MethodPost, endpoint+"/"+itoa(number)+"/merge", pullrequest.RevisionInput{
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID,
	}, "", "")
	if mergeResponse.StatusCode != http.StatusOK {
		t.Fatalf("merge status=%d error=%q", mergeResponse.StatusCode, apiErrorCode(t, mergeResponse))
	}
	merged := decodeAPISuccess(t, mergeResponse)
	if merged.PullRequest == nil || merged.PullRequest.Merge == nil || merged.PullRequest.Merge.OID != fixture.sourceOID || merged.PullRequest.Merge.Mode != "fast_forward" {
		t.Fatalf("merge response=%+v", merged.PullRequest)
	}
	if got := apiGitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "--verify", "refs/heads/main"); got != fixture.sourceOID {
		t.Fatalf("target ref=%s, want %s", got, fixture.sourceOID)
	}

	listResponse := apiRequest(t, http.MethodGet, endpoint, nil, "", "")
	if listResponse.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d", listResponse.StatusCode)
	}
	listed := decodeAPISuccess(t, listResponse)
	if len(listed.Items) != 1 || listed.Items[0].State != state.PullRequestMerged {
		t.Fatalf("list response=%+v", listed.Items)
	}
}

func TestPullRequestAPICreationReconciliationPendingReturns503(t *testing.T) {
	fixture := newAPIFixture(t, false)
	databasePath := filepath.Join(fixture.store.Dir(), "owngit.sqlite")
	// Match the store's own file URI so the second connection sees the same
	// database on every platform.
	slashPath := filepath.ToSlash(databasePath)
	if len(slashPath) >= 3 && slashPath[1] == ':' && slashPath[2] == '/' {
		slashPath = "/" + slashPath
	}
	database, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: slashPath}).String())
	noErr(t, err)
	defer database.Close()
	if _, err := database.Exec("PRAGMA busy_timeout=5000"); err != nil {
		t.Fatal(err)
	}
	// Fail only the activation update so creation reaches its explicit
	// pending-reconciliation state instead of a generic internal error.
	if _, err := database.Exec(`CREATE TRIGGER fail_activation BEFORE UPDATE ON pull_requests BEGIN SELECT RAISE(ABORT, 'injected activation failure'); END`); err != nil {
		t.Fatal(err)
	}
	server := serve(t, fixture.app.Handler())
	response := apiRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/project/pull-requests", map[string]any{
		"title": "Pending activation", "source_branch": "feature", "target_branch": "main", "review": "skip",
	}, "", "")
	code := apiErrorCode(t, response)
	if response.StatusCode != http.StatusServiceUnavailable || code != "pull_request_creation_reconciliation_pending" {
		t.Fatalf("pending creation status=%d code=%q", response.StatusCode, code)
	}
}

func TestPullRequestAPIIgnoresBrowserCookiesAndUsesGeneralPassword(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	endpoint := server.URL + "/api/v1/repositories/project/pull-requests"
	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(context.Background(), "browser-session", "general", "csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)))

	body, _ := json.Marshal(map[string]any{
		"title": "Protected feature", "source_branch": "feature", "target_branch": "main", "review": "skip",
	})
	request, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: generalCookie, Value: "browser-session"})
	response, err := http.DefaultClient.Do(request)
	noErr(t, err)
	if response.StatusCode != http.StatusUnauthorized || apiErrorCode(t, response) != "authentication_required" {
		t.Fatalf("cookie-only API auth status=%d", response.StatusCode)
	}

	admin := apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Protected feature", "source_branch": "feature", "target_branch": "main", "review": "skip",
	}, "admin-password", "")
	if admin.StatusCode != http.StatusUnauthorized || apiErrorCode(t, admin) != "invalid_credentials" {
		t.Fatalf("administrator password status=%d", admin.StatusCode)
	}
	wrong := apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Protected feature", "source_branch": "feature", "target_branch": "main", "review": "skip",
	}, "wrong-password", "")
	if wrong.StatusCode != http.StatusUnauthorized || apiErrorCode(t, wrong) != "invalid_credentials" {
		t.Fatalf("wrong password status=%d", wrong.StatusCode)
	}
	valid := apiRequest(t, http.MethodPost, endpoint, map[string]any{
		"title": "Protected feature", "source_branch": "feature", "target_branch": "main", "review": "skip",
	}, "shared-password", "")
	if valid.StatusCode != http.StatusOK {
		t.Fatalf("general password status=%d error=%q", valid.StatusCode, apiErrorCode(t, valid))
	}
}

type apiFixture struct {
	app       *App
	store     *state.Store
	remote    string
	work      string
	sourceOID string
	targetOID string
}

func newAPIFixture(t *testing.T, protected bool) apiFixture {
	t.Helper()
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	mode := "open"
	accessHash := ""
	if protected {
		mode = "password"
		var err error
		accessHash, err = auth.HashPassword("shared-password")
		noErr(t, err)
	}
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(context.Background(), repositoryRoot, mode, accessHash, adminHash, true))
	app.Repositories.SetRoot(repositoryRoot)
	stored, err := app.Repositories.Create(context.Background(), "project", "API fixture")
	noErr(t, err)
	remote, err := app.Repositories.Path(stored.ID)
	noErr(t, err)
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "API Test")
	apiRunGit(t, work, "config", "user.email", "api-test@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("base\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "base")
	apiRunGit(t, work, "remote", "add", "origin", remote)
	apiRunGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	targetOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	apiRunGit(t, work, "checkout", "-b", "feature")
	noErr(t, os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "feature")
	apiRunGit(t, work, "push", "origin", "HEAD:refs/heads/feature")
	sourceOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	app.PullRequests = &pullrequest.Service{Store: store, Repositories: app.Repositories}
	return apiFixture{app: app, store: store, remote: remote, work: work, sourceOID: sourceOID, targetOID: targetOID}
}

func apiRequest(t *testing.T, method, target string, value any, password, origin string) *http.Response {
	t.Helper()
	return sendJSON(t, method, target, value, basicAuth("owngit", password), header("Origin", origin))
}

// sendJSON sends value as a JSON body, or no body when it is nil, after
// applying each request edit.
func sendJSON(t *testing.T, method, target string, value any, edits ...func(*http.Request)) *http.Response {
	t.Helper()
	var body []byte
	if value != nil {
		var err error
		body, err = json.Marshal(value)
		noErr(t, err)
	}
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	noErr(t, err)
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, edit := range edits {
		edit(request)
	}
	response, err := http.DefaultClient.Do(request)
	noErr(t, err)
	return response
}

// header sets a request header when value is not empty.
func header(name, value string) func(*http.Request) {
	return func(request *http.Request) {
		if value != "" {
			request.Header.Set(name, value)
		}
	}
}

// basicAuth sends Basic credentials when password is not empty.
func basicAuth(user, password string) func(*http.Request) {
	return func(request *http.Request) {
		if password != "" {
			request.SetBasicAuth(user, password)
		}
	}
}

// adminCookieValue sends an administrator session cookie.
func adminCookieValue(value string) func(*http.Request) {
	return func(request *http.Request) { request.AddCookie(&http.Cookie{Name: adminCookie, Value: value}) }
}
func decodeAPISuccess(t *testing.T, response *http.Response) pullrequest.SuccessEnvelope {
	t.Helper()
	defer response.Body.Close()
	var envelope pullrequest.SuccessEnvelope
	noErr(t, json.NewDecoder(response.Body).Decode(&envelope))
	if !envelope.OK {
		t.Fatal("API success response reported ok=false")
	}
	return envelope
}

func apiErrorCode(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	var envelope pullrequest.ErrorEnvelope
	noErrf(t, json.NewDecoder(response.Body).Decode(&envelope), "decode API error")
	return envelope.Error.Code
}

func apiRunGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	if output, err := gitCombined(directory, arguments...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}
func apiGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = testfixture.GitEnvironment(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"))
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(arguments, " "), err, output, stderr.Bytes())
	}
	return strings.TrimSpace(string(output))
}

func itoa(number int64) string {
	return strconv.FormatInt(number, 10)
}
