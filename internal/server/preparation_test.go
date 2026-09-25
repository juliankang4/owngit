package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/repository"
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

// While OwnGit serves, a repository whose folder disappears is locked and
// prepared again like at startup, and one whose Git data alone cannot be read
// is only marked: the dashboard lists every repository, the cause stays in
// the server log, and both are shown normally again once they can be read
// (QA-009).
func TestUnreadableRepositoryAtRuntimeKeepsTheDashboard(t *testing.T) {
	app := newConfiguredApp(t)
	for _, name := range []string{"alpha", "gone", "broken"} {
		_, err := app.Repositories.Create(context.Background(), name, "")
		noErr(t, err)
	}
	app.Repositories.PreparationRetry = 20 * time.Millisecond
	var logMu sync.Mutex
	var logged []string
	logf := func(format string, arguments ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logged = append(logged, fmt.Sprintf(format, arguments...))
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		noErr(t, app.Repositories.StopPreparation(context.Background()))
	})
	noErr(t, app.Repositories.StartPreparation(ctx, nil, 5*time.Second, logf))

	gonePath, err := app.Repositories.Path("gone")
	noErr(t, err)
	noErr(t, os.Rename(gonePath, gonePath+".away"))
	brokenPath, err := app.Repositories.Path("broken")
	noErr(t, err)
	packedRefs := filepath.Join(brokenPath, "packed-refs")
	noErr(t, os.WriteFile(packedRefs, []byte("not a packed ref\n"), 0o600))

	var serverLog lockedLog
	previousLog, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&serverLog)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousLog)
		log.SetFlags(previousFlags)
	})
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body, status := dashboardGET(t, client, server.URL+"/")
	if status != http.StatusOK || !strings.Contains(body, "This repository is being prepared.") || !strings.Contains(body, "This repository&#39;s Git data could not be read.") {
		t.Fatalf("dashboard status=%d does not mark the unreadable repositories", status)
	}
	for _, name := range []string{"alpha", "gone", "broken"} {
		if !strings.Contains(body, `class="row row--repo" href="/repositories/`+name+`"`) {
			t.Fatalf("dashboard does not list %s", name)
		}
	}
	if strings.Contains(body, app.Repositories.RepositoryRoot()) || strings.Contains(body, "packed") {
		t.Fatal("the dashboard shows storage details")
	}
	// The activity graph leaves both out with a reason instead of becoming
	// unavailable as a whole.
	if !strings.Contains(body, "Repositories that are still being prepared or whose Git data could not be read are not counted.") || strings.Contains(body, "could not be read while counting") {
		t.Fatal("the activity graph does not leave out only the unreadable repository")
	}
	// The cause is logged once, not on every visit.
	dashboardGET(t, client, server.URL+"/")
	if count := strings.Count(serverLog.String(), `repository "broken" is shown as unreadable`); count != 1 || !strings.Contains(serverLog.String(), "packed-refs") {
		t.Fatalf("the unreadable repository was logged %d times: %q", count, serverLog.String())
	}
	if !app.Repositories.Preparing("gone") {
		t.Fatal("the repository with a missing folder is not locked")
	}
	if app.Repositories.Preparing("broken") {
		t.Fatal("a repository whose folder is readable was locked")
	}
	if body, status = dashboardGET(t, client, server.URL+"/repositories/gone"); status != http.StatusServiceUnavailable || !strings.Contains(body, "This repository is being prepared.") {
		t.Fatalf("locked repository page status=%d lacks the preparing notice", status)
	}
	// Retries keep the missing repository locked.
	time.Sleep(200 * time.Millisecond)
	if !app.Repositories.Preparing("gone") {
		t.Fatal("a repository with a missing folder was served again")
	}
	logMu.Lock()
	joined := strings.Join(logged, "\n")
	logMu.Unlock()
	if !strings.Contains(joined, `repository "gone" could not be read`) || strings.Contains(joined, `"broken"`) {
		t.Fatalf("server log does not name exactly the locked repository: %q", joined)
	}

	noErr(t, os.Rename(gonePath+".away", gonePath))
	deadline := time.Now().Add(10 * time.Second)
	for app.Repositories.Preparing("gone") {
		if time.Now().After(deadline) {
			t.Fatal("the repository was not served again after its folder returned")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if body, _ = dashboardGET(t, client, server.URL+"/"); !strings.Contains(body, "Repositories whose Git data could not be read are not counted.") {
		t.Fatal("the activity graph does not name the unreadable repository as the reason")
	}
	noErr(t, os.Remove(packedRefs))
	if body, status = dashboardGET(t, client, server.URL+"/"); status != http.StatusOK || strings.Contains(body, "This repository is being prepared.") || strings.Contains(body, "could not be read") {
		t.Fatalf("dashboard after recovery status=%d still marks a repository", status)
	}
}

// A dashboard that listed a repository just before it was deleted leaves it
// out and starts no preparation for it, so a repository created again under
// that name is served at once (review of QA-009). Slow repositories are
// simulated by holding their locks, so the deleted one is read last.
func TestDashboardDuringDeletionStartsNoPreparation(t *testing.T) {
	app := newConfiguredApp(t)
	var blockers []string
	for index := 0; index < 5*snapshotConcurrency; index++ {
		// Blockers sort before and after the deleted repository, so its read
		// queues behind theirs.
		name := fmt.Sprintf("a-blocker-%d", index)
		if index%2 == 1 {
			name = fmt.Sprintf("z-blocker-%d", index)
		}
		_, err := app.Repositories.Create(context.Background(), name, "")
		noErr(t, err)
		blockers = append(blockers, name)
	}
	app.Repositories.PreparationRetry = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		noErr(t, app.Repositories.StopPreparation(context.Background()))
	})
	noErr(t, app.Repositories.StartPreparation(ctx, nil, 5*time.Second, func(string, ...any) {}))
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// A round whose page read the repository before the deletion rightly
	// lists it and proves nothing, so it is void and another round runs. At
	// least one round must read after the deletion, within a bounded time.
	raced, rounds, deadline := 0, 0, time.Now().Add(90*time.Second)
	for ; raced < 2 && rounds < 20 && time.Now().Before(deadline); rounds++ {
		round := rounds
		doomed := fmt.Sprintf("m-doomed-%d", round)
		_, err := app.Repositories.Create(context.Background(), doomed, "")
		noErr(t, err)
		for _, name := range blockers {
			// Advance the generation first so no cached snapshot is used.
			lock := app.Repositories.Locks.For(name)
			lock.Lock()
			lock.Unlock()
			lock.Lock()
		}
		type page struct {
			body   string
			status int
		}
		done := make(chan page, 1)
		go func() {
			body, status := dashboardGET(t, client, server.URL+"/")
			done <- page{body, status}
		}()
		time.Sleep(300 * time.Millisecond)
		_, err = app.Repositories.Delete(context.Background(), doomed, repository.DeleteKeepFiles)
		noErr(t, err)
		for _, name := range blockers {
			app.Repositories.Locks.For(name).UnlockWithoutRefChanges()
		}
		result := <-done
		if result.status != http.StatusOK {
			t.Fatalf("round %d: dashboard status=%d", round, result.status)
		}
		if row, listed := dashboardRow(result.body, doomed); !listed {
			raced++
		} else if strings.Contains(row, "This repository is being prepared.") || strings.Contains(row, "could not be read") {
			t.Fatalf("round %d: the deleted repository is listed as unreadable or preparing", round)
		}
		time.Sleep(100 * time.Millisecond)
		if app.Repositories.Preparing(doomed) {
			t.Fatalf("round %d: the deleted repository is being prepared", round)
		}
		_, err = app.Repositories.Create(context.Background(), doomed, "")
		noErr(t, err)
		if _, _, _, err := app.Repositories.ExistingPath(context.Background(), doomed); err != nil {
			t.Fatalf("round %d: the recreated repository is refused: %v", round, err)
		}
	}
	t.Logf("%d of %d rounds read the repository after its deletion", raced, rounds)
	if raced == 0 {
		t.Fatal("no round read the repository after its deletion")
	}
}

// dashboardRow returns the dashboard list row of repository id.
func dashboardRow(body, id string) (string, bool) {
	start := strings.Index(body, `class="row row--repo" href="/repositories/`+id+`"`)
	if start < 0 {
		return "", false
	}
	row := body[start:]
	if end := strings.Index(row, "</a>"); end >= 0 {
		row = row[:end]
	}
	return row, true
}

// lockedLog collects the standard logger's output for one test.
type lockedLog struct {
	mu     sync.Mutex
	buffer strings.Builder
}

func (l *lockedLog) Write(content []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.Write(content)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.String()
}
