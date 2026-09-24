package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/webui"
)

// A repository whose preparation fails after startup is refused with a fixed
// notice on every surface, while another repository is served, and is served
// again once a later attempt succeeds.
func TestPreparingRepositoryIsRefusedWhileOthersServe(t *testing.T) {
	app := newConfiguredApp(t)
	app.PullRequests = &pullrequest.Service{Store: app.Store, Repositories: app.Repositories}
	for _, name := range []string{"ready-one", "stuck"} {
		_, err := app.Repositories.Create(context.Background(), name, "")
		noErr(t, err)
	}
	var failing atomic.Bool
	failing.Store(true)
	const detail = "synthetic-preparation-detail"
	step := func(_ context.Context, id, _ string) error {
		if id == "stuck" && failing.Load() {
			return errors.New(detail)
		}
		return nil
	}
	app.Repositories.PreparationRetry = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		noErr(t, app.Repositories.StopPreparation(context.Background()))
	})
	noErr(t, app.Repositories.StartPreparation(ctx, step, 5*time.Second, func(string, ...any) {}))

	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	get := func(target string) (int, string, http.Header) {
		t.Helper()
		response, err := client.Get(server.URL + target)
		noErr(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		noErr(t, err)
		return response.StatusCode, string(body), response.Header
	}

	body, status := dashboardGET(t, client, server.URL+"/")
	if status != http.StatusOK || !strings.Contains(body, "ready-one") || !strings.Contains(body, "This repository is being prepared.") {
		t.Fatalf("dashboard status=%d did not list both repositories with the preparing status", status)
	}
	if !strings.Contains(body, "Repositories that are still being prepared are not counted yet.") || strings.Contains(body, "could not be read while counting") {
		t.Fatal("the dashboard activity does not explain the skipped repository")
	}
	body, status = dashboardGET(t, client, server.URL+"/repositories/stuck")
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "This repository is being prepared.") || !strings.Contains(body, "OwnGit keeps retrying on its own.") {
		t.Fatalf("preparing repository page status=%d lacks the notice", status)
	}
	if body, _ = dashboardGET(t, client, server.URL+"/repositories/stuck/pull-requests?lang=ko"); !strings.Contains(body, "이 저장소를 준비하는 중입니다.") {
		t.Fatal("the Korean notice is missing")
	}
	if _, status = dashboardGET(t, client, server.URL+"/repositories/ready-one?lang=en"); status != http.StatusOK {
		t.Fatalf("ready repository page status=%d", status)
	}

	status, body, header := get("/git/stuck.git/info/refs?service=git-upload-pack")
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "being prepared") || header.Get("Retry-After") == "" {
		t.Fatalf("Git HTTP for a preparing repository status=%d body=%q", status, body)
	}
	if status, _, _ = get("/git/ready-one.git/info/refs?service=git-upload-pack"); status != http.StatusOK {
		t.Fatalf("Git HTTP for a ready repository status=%d", status)
	}

	status, body, _ = get("/api/v1/repositories/stuck/pull-requests")
	var problem struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	noErr(t, json.Unmarshal([]byte(body), &problem))
	if status != http.StatusServiceUnavailable || problem.Error.Code != "repository_preparing" {
		t.Fatalf("API for a preparing repository status=%d body=%s", status, body)
	}
	if status, _, _ = get("/api/v1/repositories/ready-one/pull-requests"); status != http.StatusOK {
		t.Fatalf("API for a ready repository status=%d", status)
	}
	for _, target := range []string{"/", "/repositories/stuck", "/git/stuck.git/info/refs?service=git-upload-pack", "/api/v1/repositories/stuck/pull-requests"} {
		if _, body, _ := get(target); strings.Contains(body, detail) {
			t.Fatalf("%s shows the preparation error detail", target)
		}
	}

	failing.Store(false)
	deadline := time.Now().Add(10 * time.Second)
	for app.Repositories.Preparing("stuck") {
		if time.Now().After(deadline) {
			t.Fatal("the repository did not become ready after preparation succeeded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status, _, _ = get("/git/stuck.git/info/refs?service=git-upload-pack"); status != http.StatusOK {
		t.Fatalf("Git HTTP after preparation status=%d", status)
	}
	if _, status = dashboardGET(t, client, server.URL+"/repositories/stuck?lang=en"); status != http.StatusOK {
		t.Fatalf("repository page after preparation status=%d", status)
	}
}

// An administrator can delete a repository while it is being prepared, from
// the same confirmed page as any other.
func TestPreparingRepositoryCanBeDeletedFromTheBrowser(t *testing.T) {
	fixture := newAPIFixture(t, false)
	app := fixture.app
	// The browser opens the repository page before preparation starts.
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	app.Repositories.PreparationRetry = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		noErr(t, app.Repositories.StopPreparation(context.Background()))
	})
	failing := func(context.Context, string, string) error { return errors.New("synthetic preparation failure") }
	noErr(t, app.Repositories.StartPreparation(ctx, failing, 5*time.Second, func(string, ...any) {}))
	if !app.Repositories.Preparing("project") {
		t.Fatal("fixture repository is not preparing")
	}
	if page := browserGET(t, client, server.URL+"/repositories/project/delete"); page.status != http.StatusOK {
		t.Fatalf("delete page of a preparing repository status=%d", page.status)
	}
	result := browserForm(t, client, server.URL+"/repositories/project/delete", url.Values{
		"csrf": {adminTestCSRF}, "mode": {"keep_files"}, "confirm_name": {"project"}, "admin_password": {"admin-password"},
	}, server.URL)
	if result.status != http.StatusSeeOther {
		t.Fatalf("delete of a preparing repository status=%d body=%s", result.status, result.body)
	}
	if _, exists, err := fixture.store.Repository(t.Context(), "project"); err != nil || exists {
		t.Fatalf("repository record remains: exists=%v err=%v", exists, err)
	}
	if app.Repositories.Preparing("project") {
		t.Fatal("the deleted repository is still preparing")
	}
}

// The credential screens stay usable while a repository is being prepared,
// so an administrator can revoke a credential from the browser as through
// the API. They read and change only the state database: no Git runs.
func TestPreparingRepositoryCredentialScreensRevokeWithoutGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-recording wrapper is a Unix test fixture")
	}
	fixture := newAPIFixture(t, false)
	app := fixture.app
	server, client, jar := openBrowser(t, fixture)
	csrf := browserAdminSessionFor(t, fixture, server.URL, jar, "prep-admin")
	if result := browserForm(t, client, server.URL+configuredChecksURL("project"), validPolicyValues(csrf), server.URL); result.status != http.StatusSeeOther {
		t.Fatalf("policy save status=%d", result.status)
	}
	helper, _, created, err := app.issueHelperCredential(context.Background(), "project", "prep helper", "")
	if err != nil || !created {
		t.Fatalf("issue helper credential created=%v err=%v", created, err)
	}
	runner, _, created, err := fixture.store.IssueCheckRunnerToken(context.Background(), "project", "prep runner", "", time.Now())
	if err != nil || !created {
		t.Fatalf("issue runner token created=%v err=%v", created, err)
	}

	trace := filepath.Join(t.TempDir(), "git-trace")
	useGitWrapper(t, app, "printf '%s\\n' \"$*\" >> "+serverShellQuote(trace))
	var attempts atomic.Int32
	app.Repositories.PreparationRetry = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		noErr(t, app.Repositories.StopPreparation(context.Background()))
	})
	noErr(t, app.Repositories.StartPreparation(ctx, func(context.Context, string, string) error {
		attempts.Add(1)
		return errors.New("synthetic preparation failure")
	}, 5*time.Second, func(string, ...any) {}))
	if attempts.Load() != 1 || !app.Repositories.Preparing("project") {
		t.Fatalf("fixture repository is not preparing after one attempt (attempts=%d)", attempts.Load())
	}
	before, err := os.ReadFile(trace)
	noErr(t, err)
	if !strings.Contains(string(before), "config --local --list") {
		t.Fatalf("the Git trace did not record the preparation attempt:\n%s", before)
	}

	helperURL := server.URL + baseHelperCredentialsURL("project")
	if page := browserGET(t, client, helperURL); page.status != http.StatusOK || !strings.Contains(page.body, "prep helper") {
		t.Fatalf("helper credential screen of a preparing repository status=%d", page.status)
	}
	revoked := browserForm(t, client, helperURL, url.Values{
		"csrf": {csrf}, "action": {webui.ActionRevokeHelperCredential}, "credential_id": {helper.ID}, "admin_password": {"admin-password"},
	}, server.URL)
	if revoked.status != http.StatusSeeOther {
		t.Fatalf("helper credential revoke status=%d", revoked.status)
	}
	helpers, err := fixture.store.HelperCredentials(context.Background(), "project")
	if err != nil || len(helpers) != 1 || helpers[0].RevokedAt == nil {
		t.Fatalf("helper revocation not recorded: %+v err=%v", helpers, err)
	}

	tokenURL := server.URL + runnerTokensURL("project")
	if page := browserGET(t, client, tokenURL); page.status != http.StatusOK || !strings.Contains(page.body, "prep runner") {
		t.Fatalf("runner token screen of a preparing repository status=%d", page.status)
	}
	revoked = browserForm(t, client, tokenURL, url.Values{
		"csrf": {csrf}, "action": {webui.ActionRevokeRunnerToken}, "credential_id": {runner.ID}, "admin_password": {"admin-password"},
	}, server.URL)
	if revoked.status != http.StatusSeeOther {
		t.Fatalf("runner token revoke status=%d", revoked.status)
	}
	runners, err := fixture.store.CheckRunnerCredentials(context.Background(), "project")
	if err != nil || len(runners) != 1 || runners[0].RevokedAt == nil {
		t.Fatalf("runner revocation not recorded: %+v err=%v", runners, err)
	}

	// The administrator checks still apply: no password, no change.
	if result := browserForm(t, client, tokenURL, url.Values{
		"csrf": {csrf}, "action": {webui.ActionRevokeRunnerToken}, "credential_id": {runner.ID},
	}, server.URL); result.status != http.StatusUnauthorized {
		t.Fatalf("revoke without the password status=%d", result.status)
	}
	// Other screens of the repository stay behind the notice.
	if page := browserGET(t, client, server.URL+configuredChecksURL("project")); page.status != http.StatusServiceUnavailable {
		t.Fatalf("configured checks screen of a preparing repository status=%d", page.status)
	}
	after, err := os.ReadFile(trace)
	noErr(t, err)
	if string(after) != string(before) {
		t.Fatalf("the credential screens ran Git on a preparing repository:\n%s", strings.TrimPrefix(string(after), string(before)))
	}
	if attempts.Load() != 1 {
		t.Fatalf("preparation ran %d attempts, want 1", attempts.Load())
	}
}

// A deletion refused because a preparation attempt holds the repository says
// so, instead of naming a push or a clone.
func TestDeletionDuringAPreparationAttemptExplainsTheWait(t *testing.T) {
	fixture := newAPIFixture(t, false)
	app := fixture.app
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	release := make(chan struct{})
	var entered atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		noErr(t, app.Repositories.StopPreparation(context.Background()))
	})
	defer close(release)
	noErr(t, app.Repositories.StartPreparation(ctx, func(ctx context.Context, _, _ string) error {
		entered.Store(true)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return errors.New("synthetic preparation failure")
	}, 10*time.Millisecond, func(string, ...any) {}))
	deadline := time.Now().Add(10 * time.Second)
	for !entered.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the preparation attempt did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, language := range []struct{ lang, text string }{
		{"en", "OwnGit is preparing this repository right now."},
		{"ko", "OwnGit이 지금 이 저장소를 준비하고 있습니다."},
	} {
		result := browserForm(t, client, server.URL+"/repositories/project/delete?lang="+language.lang, url.Values{
			"csrf": {adminTestCSRF}, "mode": {"keep_files"}, "confirm_name": {"project"}, "admin_password": {"admin-password"},
		}, server.URL)
		if result.status != http.StatusConflict || !strings.Contains(result.body, language.text) || strings.Contains(result.body, "such as a push or a clone") {
			t.Fatalf("%s deletion during a preparation attempt status=%d lacks the preparing reason", language.lang, result.status)
		}
	}
	if _, exists, err := fixture.store.Repository(t.Context(), "project"); err != nil || !exists {
		t.Fatalf("a refused deletion removed the repository: exists=%v err=%v", exists, err)
	}
}
