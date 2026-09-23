package server

import (
	"context"
	"crypto/sha256"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/state"
	"owngit/internal/webui"
)

type browserHTTPResult struct {
	status int
	header http.Header
	body   string
}

func TestBrowserPullRequestWorkflowUsesObservedHeadsAndAdvisoryEvidence(t *testing.T) {
	fixture := newAPIFixture(t, false)
	fixture.app.Version = "1.0.0-browser-test"
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)

	repositoryPage := browserGET(t, client, server.URL+"/repositories/project")
	if repositoryPage.status != http.StatusOK || !strings.Contains(repositoryPage.body, "1.0.0-browser-test") ||
		!strings.Contains(repositoryPage.body, "/repositories/project/pull-requests") || !strings.Contains(repositoryPage.body, "/repositories/project/tasks") {
		t.Fatalf("repository navigation/version status=%d", repositoryPage.status)
	}
	csrf := cookieValue(t, jar, server.URL, generalCookie)
	newURL := server.URL + "/repositories/project/pull-requests/new?source=feature&target=main"
	observed := browserGET(t, client, newURL)
	if observed.status != http.StatusOK || !strings.Contains(observed.body, `name="source_oid" value="`+fixture.sourceOID+`"`) ||
		!strings.Contains(observed.body, `name="target_oid" value="`+fixture.targetOID+`"`) || !strings.Contains(observed.body, "feature.txt") {
		t.Fatalf("observed create page status=%d", observed.status)
	}

	createURL := server.URL + "/repositories/project/pull-requests"
	values := url.Values{
		"title":         {"Browser workflow"},
		"source_branch": {"feature"},
		"target_branch": {"main"},
		"source_oid":    {fixture.sourceOID},
		"target_oid":    {fixture.targetOID},
		"review":        {webui.ReviewChoiceRequest},
	}
	missingCSRF := browserForm(t, client, createURL, values, server.URL)
	if missingCSRF.status != http.StatusForbidden {
		t.Fatalf("create without csrf status=%d", missingCSRF.status)
	}
	if records, err := fixture.store.PullRequests(context.Background(), "project"); err != nil || len(records) != 0 {
		t.Fatalf("missing csrf changed pull requests: count=%d err=%v", len(records), err)
	}

	values.Set("csrf", csrf)
	values.Set("source_oid", strings.Repeat("a", 40))
	forged := browserForm(t, client, createURL, values, server.URL)
	if forged.status != http.StatusConflict || !strings.Contains(forged.body, "branch moved") {
		t.Fatalf("forged head status=%d", forged.status)
	}

	noErr(t, os.WriteFile(fixture.work+"/feature.txt", []byte("feature moved\n"), 0o600))
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "move feature")
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/feature")
	movedSource := apiGitOutput(t, fixture.work, "rev-parse", "HEAD")
	values.Set("source_oid", fixture.sourceOID)
	stale := browserForm(t, client, createURL, values, server.URL)
	if stale.status != http.StatusConflict || !strings.Contains(stale.body, movedSource[:10]) {
		t.Fatalf("stale observed head status=%d", stale.status)
	}

	values.Set("source_oid", movedSource)
	created := browserForm(t, client, createURL, values, server.URL)
	if created.status != http.StatusSeeOther || created.header.Get("Location") != "/repositories/project/pull-requests/1?notice=pull_request_created" {
		t.Fatalf("create status=%d location=%q", created.status, created.header.Get("Location"))
	}
	detail := browserGET(t, client, server.URL+created.header.Get("Location"))
	if detail.status != http.StatusOK || !strings.Contains(detail.body, "Browser workflow") || !strings.Contains(detail.body, "Review requested") || !strings.Contains(detail.body, "feature.txt") {
		t.Fatalf("created detail status=%d", detail.status)
	}

	if _, err := fixture.app.Repositories.Create(context.Background(), "other", "Other repository"); err != nil {
		t.Fatal(err)
	}
	crossRepository := browserGET(t, client, server.URL+"/repositories/other/pull-requests/1")
	if crossRepository.status != http.StatusNotFound || strings.Contains(crossRepository.body, "Browser workflow") {
		t.Fatalf("cross-repository pull request status=%d", crossRepository.status)
	}

	task, err := fixture.store.CreateTask(context.Background(), "project", "Pending advisory check", time.Now().UTC())
	noErr(t, err)
	pending := state.CheckAttempt{
		ID: "11111111111111111111111111111111", TaskID: task.ID, RepositoryID: "project", RevisionOID: movedSource,
		WorktreeState: state.WorktreeClean, StartedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), CredentialID: "browser-helper",
		Checks: []state.CheckDefinition{{Name: "test", Command: "go test ./..."}},
	}
	if _, _, err := fixture.store.RegisterCheckAttempt(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	detail = browserGET(t, client, server.URL+"/repositories/project/pull-requests/1")
	if detail.status != http.StatusOK || !strings.Contains(detail.body, "Registered, no result yet") ||
		!strings.Contains(detail.body, "does not track whether it is still running") || !strings.Contains(detail.body, "?task="+task.ID) {
		t.Fatalf("pending evidence detail status=%d", detail.status)
	}

	merge := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/merge", url.Values{
		"csrf": {csrf}, "source_oid": {movedSource}, "target_oid": {fixture.targetOID},
	}, server.URL)
	if merge.status != http.StatusSeeOther || merge.header.Get("Location") != "/repositories/project/pull-requests/1?notice=pull_request_merged" {
		t.Fatalf("advisory merge status=%d location=%q", merge.status, merge.header.Get("Location"))
	}
	if got := apiGitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main"); got != movedSource {
		t.Fatalf("merged target=%q want moved source", got)
	}
	merged := browserGET(t, client, server.URL+merge.header.Get("Location"))
	mergedNotice := strings.Contains(merged.body, "Merged.")
	mergedPending := strings.Contains(merged.body, "Registered, no result yet")
	if merged.status != http.StatusOK || !mergedNotice || !mergedPending {
		t.Fatalf("merged detail status=%d notice=%v pending=%v", merged.status, mergedNotice, mergedPending)
	}
}

func TestBrowserTaskEvidenceShowsStaleDirtyCleanupAndRepositoryBinding(t *testing.T) {
	fixture := newAPIFixture(t, false)
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "project", Title: "Evidence mapping", SourceBranch: "feature", TargetBranch: "main",
		SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID,
	})
	noErr(t, err)
	server, client, jar := openBrowser(t, fixture)

	staleTask, err := fixture.store.CreateTask(context.Background(), "project", "Older revision", time.Now().UTC().Add(-time.Minute))
	noErr(t, err)
	recordBrowserAttempt(t, fixture.store, staleTask.ID, fixture.targetOID, "22222222222222222222222222222222", state.WorktreeClean, "", false)
	// Evidence for another branch is not an earlier revision of this change.
	unrelated := browserGET(t, client, server.URL+pullRequestURL("project", created.Number))
	if unrelated.status != http.StatusOK || strings.Contains(unrelated.body, "newest result is for an earlier revision") {
		t.Fatalf("unrelated evidence shown as an earlier revision: status=%d", unrelated.status)
	}
	// Once the pull request has recorded that revision as its source, the same
	// evidence is its own earlier result.
	if err := fixture.store.RecordPullRequestRevision(context.Background(), state.PullRequestRevision{
		RepositoryID: "project", PullRequestNumber: created.Number, SourceOID: fixture.targetOID, TargetOID: fixture.targetOID,
		RecordedAt: time.Now().UTC().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	staleDetail := browserGET(t, client, server.URL+pullRequestURL("project", created.Number))
	if staleDetail.status != http.StatusOK || !strings.Contains(staleDetail.body, "newest result is for an earlier revision") ||
		!strings.Contains(staleDetail.body, "says nothing about the code") || strings.Contains(staleDetail.body, "The working copy matched this commit") {
		t.Fatalf("stale pull request evidence status=%d", staleDetail.status)
	}

	cleanupTask, err := fixture.store.CreateTask(context.Background(), "project", "Cleanup evidence", time.Now().UTC())
	noErr(t, err)
	cleanupReason := "owned process <still-present>"
	recordBrowserAttempt(t, fixture.store, cleanupTask.ID, fixture.sourceOID, "33333333333333333333333333333333", state.WorktreeDirty, cleanupReason, true)
	cleanupDetail := browserGET(t, client, server.URL+pullRequestURL("project", created.Number))
	cleanupLabel := strings.Contains(cleanupDetail.body, "A check could not finish")
	cleanupDirty := strings.Contains(cleanupDetail.body, "had uncommitted changes")
	cleanupWarning := strings.Contains(cleanupDetail.body, "could not confirm")
	cleanupClaim := strings.Contains(cleanupDetail.body, "The working copy matched this commit")
	if cleanupDetail.status != http.StatusOK || !cleanupLabel || !cleanupDirty || !cleanupWarning || cleanupClaim {
		t.Fatalf("cleanup pull request evidence status=%d label=%v dirty=%v warning=%v tested_claim=%v", cleanupDetail.status, cleanupLabel, cleanupDirty, cleanupWarning, cleanupClaim)
	}
	cleanupMerge := browserForm(t, client, server.URL+pullRequestURL("project", created.Number)+"/merge", url.Values{
		"csrf":       {cookieValue(t, jar, server.URL, generalCookie)},
		"source_oid": {fixture.sourceOID},
		"target_oid": {fixture.targetOID},
	}, server.URL)
	if cleanupMerge.status != http.StatusSeeOther {
		t.Fatalf("cleanup evidence blocked advisory merge: status=%d", cleanupMerge.status)
	}

	tasks := browserGET(t, client, server.URL+tasksURL("project", cleanupTask.ID))
	if tasks.status != http.StatusOK || !strings.Contains(tasks.body, "Cleanup evidence") || !strings.Contains(tasks.body, "Registration order") ||
		!strings.Contains(tasks.body, "Exit code") || !strings.Contains(tasks.body, "repository credential") ||
		!strings.Contains(tasks.body, "owned process &lt;still-present&gt;") || strings.Contains(tasks.body, cleanupReason) {
		t.Fatalf("task detail evidence status=%d", tasks.status)
	}

	if _, err := fixture.app.Repositories.Create(context.Background(), "other", "Other repository"); err != nil {
		t.Fatal(err)
	}
	crossRepository := browserGET(t, client, server.URL+tasksURL("other", cleanupTask.ID))
	if crossRepository.status != http.StatusNotFound || strings.Contains(crossRepository.body, "Cleanup evidence") {
		t.Fatalf("cross-repository task status=%d", crossRepository.status)
	}
}

func TestBrowserEvidenceReadFailuresStayLocalizedAndAdvisory(t *testing.T) {
	fixture := newAPIFixture(t, false)
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "project", Title: "Unreadable advisory evidence",
		SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "request",
	})
	noErr(t, err)
	noErr(t, fixture.store.Exec(context.Background(), `DROP TABLE check_configurations`))
	noErr(t, fixture.store.Exec(context.Background(), `DROP TABLE pull_request_reviews`))

	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	result := browserGET(t, client, server.URL+pullRequestURL("project", created.Number)+"?lang=ko")
	if result.status != http.StatusOK {
		t.Fatalf("read-failure detail status=%d", result.status)
	}
	checkRead := `data-en="The check configuration could not be read" data-ko="체크 설정을 읽지 못했습니다">체크 설정을 읽지 못했습니다</span>`
	reviewRead := `data-en="The review record could not be read" data-ko="리뷰 기록을 읽지 못했습니다">리뷰 기록을 읽지 못했습니다</span>`
	if !strings.Contains(result.body, checkRead) || !strings.Contains(result.body, reviewRead) {
		t.Fatalf("localized read failures check=%v review=%v", strings.Contains(result.body, checkRead), strings.Contains(result.body, reviewRead))
	}
	for _, falseClaim := range []string{
		"No checks are configured for this repository yet.", "이 저장소에는 아직 설정된 체크가 없습니다.",
		`data-en="The review could not run"`, `data-ko="리뷰를 실행하지 못했습니다"`,
	} {
		if strings.Contains(result.body, falseClaim) {
			t.Fatalf("read-failure page contains false claim %q", falseClaim)
		}
	}
	merge := browserForm(t, client, server.URL+pullRequestURL("project", created.Number)+"/merge", url.Values{
		"csrf":       {cookieValue(t, jar, server.URL, generalCookie)},
		"source_oid": {fixture.sourceOID},
		"target_oid": {fixture.targetOID},
	}, server.URL)
	if merge.status != http.StatusSeeOther {
		t.Fatalf("advisory read failures blocked merge: status=%d", merge.status)
	}
}

func TestBrowserHelperCredentialsRequireSessionPasswordAndDeliverTokenOnce(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)

	helperURL := server.URL + baseHelperCredentialsURL("project")
	withoutAdmin := browserGET(t, client, helperURL)
	if withoutAdmin.status != http.StatusSeeOther || !strings.HasPrefix(withoutAdmin.header.Get("Location"), "/admin/login") {
		t.Fatalf("helper list without admin status=%d location=%q", withoutAdmin.status, withoutAdmin.header.Get("Location"))
	}

	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	const sessionToken = "browser-admin-session"
	const adminCSRF = "browser-admin-csrf"
	noErr(t, fixture.store.CreateSession(context.Background(), sessionToken, "admin", adminCSRF, settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	parsedServer, _ := url.Parse(server.URL)
	jar.SetCookies(parsedServer, []*http.Cookie{{Name: adminCookie, Value: sessionToken, Path: "/"}})
	listed := browserGET(t, client, helperURL)
	if listed.status != http.StatusOK || !strings.Contains(listed.body, `name="csrf" value="`+adminCSRF+`"`) {
		t.Fatalf("admin helper list status=%d", listed.status)
	}

	issueValues := url.Values{"csrf": {adminCSRF}, "action": {webui.ActionIssueHelperCredential}, "label": {"browser helper"}}
	missingPassword := browserForm(t, client, helperURL, issueValues, server.URL)
	if missingPassword.status != http.StatusUnauthorized {
		t.Fatalf("remembered session issued without password: status=%d", missingPassword.status)
	}
	credentials, err := fixture.store.HelperCredentials(context.Background(), "project")
	if err != nil || len(credentials) != 0 {
		t.Fatalf("password bypass changed credentials: count=%d err=%v", len(credentials), err)
	}

	issueValues.Set("admin_password", "admin-password")
	issueValues.Set("csrf", "wrong-csrf")
	missingCSRF := browserForm(t, client, helperURL, issueValues, server.URL)
	if missingCSRF.status != http.StatusForbidden {
		t.Fatalf("credential issue with wrong csrf status=%d", missingCSRF.status)
	}
	issueValues.Set("csrf", adminCSRF)
	issued := browserForm(t, client, helperURL, issueValues, server.URL)
	if issued.status != http.StatusOK || issued.header.Get("Cache-Control") != "no-store" || issued.header.Get("Location") != "" {
		t.Fatalf("credential issue status=%d cache=%q location=%q", issued.status, issued.header.Get("Cache-Control"), issued.header.Get("Location"))
	}
	token := issuedTokenFromBody(t, issued.body)
	credentials, err = fixture.store.HelperCredentials(context.Background(), "project")
	if err != nil || len(credentials) != 1 || credentials[0].Label != "browser helper" {
		t.Fatalf("issued credential count=%d err=%v", len(credentials), err)
	}
	listed = browserGET(t, client, helperURL)
	if listed.status != http.StatusOK || strings.Contains(listed.body, token) {
		t.Fatalf("later credential GET retained the issued token: status=%d", listed.status)
	}

	if _, err := fixture.app.Repositories.Create(context.Background(), "other", "Other repository"); err != nil {
		t.Fatal(err)
	}
	crossRevoke := browserForm(t, client, server.URL+baseHelperCredentialsURL("other"), url.Values{
		"csrf": {adminCSRF}, "action": {webui.ActionRevokeHelperCredential}, "credential_id": {credentials[0].ID}, "admin_password": {"admin-password"},
	}, server.URL)
	if crossRevoke.status != http.StatusConflict {
		t.Fatalf("cross-repository credential revoke status=%d", crossRevoke.status)
	}
	credentials, _ = fixture.store.HelperCredentials(context.Background(), "project")
	if credentials[0].RevokedAt != nil {
		t.Fatal("cross-repository revoke changed the credential")
	}

	bypassRevoke := browserForm(t, client, helperURL, url.Values{
		"csrf": {adminCSRF}, "action": {webui.ActionRevokeHelperCredential}, "credential_id": {credentials[0].ID},
	}, server.URL)
	if bypassRevoke.status != http.StatusUnauthorized {
		t.Fatalf("remembered session revoked without password: status=%d", bypassRevoke.status)
	}
	validRevoke := browserForm(t, client, helperURL, url.Values{
		"csrf": {adminCSRF}, "action": {webui.ActionRevokeHelperCredential}, "credential_id": {credentials[0].ID}, "admin_password": {"admin-password"},
	}, server.URL)
	if validRevoke.status != http.StatusSeeOther || validRevoke.header.Get("Location") != baseHelperCredentialsURL("project")+"?notice=helper_credential_revoked" {
		t.Fatalf("credential revoke status=%d location=%q", validRevoke.status, validRevoke.header.Get("Location"))
	}
	hash := sha256.Sum256([]byte(token))
	if _, ok, err := fixture.store.HelperCredentialByToken(context.Background(), hash[:], time.Now()); err != nil || ok {
		t.Fatalf("revoked token remained valid: ok=%v err=%v", ok, err)
	}
}

func TestBrowserHelperCredentialLabelUTF8ByteBoundaries(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	const sessionToken = "label-boundary-admin-session"
	const adminCSRF = "label-boundary-admin-csrf"
	noErr(t, fixture.store.CreateSession(context.Background(), sessionToken, "admin", adminCSRF, settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	parsedServer, _ := url.Parse(server.URL)
	jar.SetCookies(parsedServer, []*http.Cookie{{Name: adminCookie, Value: sessionToken, Path: "/"}})

	tests := []struct {
		name     string
		label    string
		lang     string
		accepted bool
		message  string
	}{
		{name: "ascii_100", label: strings.Repeat("a", 100), lang: "en", accepted: true},
		{name: "ascii_101", label: strings.Repeat("a", 101), lang: "en", message: "Enter a single-line label between 1 and 100 UTF-8 bytes."},
		{name: "hangul_33", label: strings.Repeat("한", 33), lang: "ko", accepted: true},
		{name: "hangul_34", label: strings.Repeat("한", 34), lang: "ko", message: "한 줄 이름을 UTF-8 기준 100바이트 이내로 입력하세요. 한글만 쓰면 최대 33자입니다."},
		{name: "emoji_25", label: strings.Repeat("😀", 25), lang: "en", accepted: true},
		{name: "emoji_26", label: strings.Repeat("😀", 26), lang: "en", message: "Enter a single-line label between 1 and 100 UTF-8 bytes."},
	}
	acceptedCount := 0
	for _, test := range tests {
		result := browserForm(t, client, server.URL+baseHelperCredentialsURL("project")+"?lang="+test.lang, url.Values{
			"csrf": {adminCSRF}, "action": {webui.ActionIssueHelperCredential}, "label": {test.label}, "admin_password": {"admin-password"},
		}, server.URL)
		if test.accepted {
			if result.status != http.StatusOK || !strings.Contains(result.body, `data-select-on-focus`) {
				t.Fatalf("%s accepted boundary status=%d token_field=%v", test.name, result.status, strings.Contains(result.body, `data-select-on-focus`))
			}
			acceptedCount++
			continue
		}
		if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, test.message) || strings.Contains(result.body, `data-select-on-focus`) {
			t.Fatalf("%s rejected boundary status=%d message=%v token_field=%v", test.name, result.status, strings.Contains(result.body, test.message), strings.Contains(result.body, `data-select-on-focus`))
		}
	}
	credentials, err := fixture.store.HelperCredentials(context.Background(), "project")
	if err != nil || len(credentials) != acceptedCount {
		t.Fatalf("stored boundary credentials=%d want=%d err=%v", len(credentials), acceptedCount, err)
	}
	for _, credential := range credentials {
		if len(credential.Label) == 0 || len(credential.Label) > 100 {
			t.Fatalf("stored label byte length=%d", len(credential.Label))
		}
	}
}

func TestProtectedBrowserWorkflowRequiresGeneralSession(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	result := browserGET(t, client, server.URL+"/repositories/project/pull-requests")
	if result.status != http.StatusSeeOther || !strings.HasPrefix(result.header.Get("Location"), "/login?next=") {
		t.Fatalf("protected workflow status=%d location=%q", result.status, result.header.Get("Location"))
	}

	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(context.Background(), "protected-admin-session", "admin", "protected-admin-csrf", settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	parsedServer, _ := url.Parse(server.URL)
	jar.SetCookies(parsedServer, []*http.Cookie{{Name: adminCookie, Value: "protected-admin-session", Path: "/"}})
	adminOnly := browserGET(t, client, server.URL+baseHelperCredentialsURL("project"))
	if adminOnly.status != http.StatusOK || !strings.Contains(adminOnly.body, "Check helper credentials") {
		t.Fatalf("protected admin-only helper list status=%d", adminOnly.status)
	}
}

func recordBrowserAttempt(t *testing.T, store *state.Store, taskID, revisionOID, attemptID, worktree, cleanupReason string, completed bool) {
	t.Helper()
	now := time.Now().UTC()
	attempt := state.CheckAttempt{
		ID: attemptID, TaskID: taskID, RepositoryID: "project", RevisionOID: revisionOID,
		WorktreeState: worktree, StartedAt: now.Add(-time.Second), CreatedAt: now.Add(-time.Second), CredentialID: "authenticated-browser-helper",
		Checks: []state.CheckDefinition{{Name: "test", Command: "go test ./..."}},
	}
	if _, _, err := store.RegisterCheckAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if !completed {
		return
	}
	exitCode := 0
	if _, _, err := store.CompleteCheckAttempt(context.Background(), state.CheckCompletion{
		AttemptID: attemptID, RepositoryID: "project", TaskID: taskID, FinishedAt: now,
		WorktreeState: worktree, Log: "synthetic test output", Results: []state.CheckResult{{
			Name: "test", Command: "go test ./...", Status: state.AttemptPassed, ExitCode: &exitCode,
			DurationMS: 50, OutputExcerpt: "ok", CleanupError: cleanupReason,
		}},
	}, now); err != nil {
		t.Fatal(err)
	}
}

func newBrowserClient(t *testing.T) (*http.Client, http.CookieJar) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	noErr(t, err)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, jar
}

func browserGET(t *testing.T, client *http.Client, target string) browserHTTPResult {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	noErr(t, err)
	return browserRequest(t, client, request)
}

func browserForm(t *testing.T, client *http.Client, target string, values url.Values, origin string) browserHTTPResult {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
	noErr(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", origin)
	return browserRequest(t, client, request)
}

func browserRequest(t *testing.T, client *http.Client, request *http.Request) browserHTTPResult {
	t.Helper()
	response, err := client.Do(request)
	noErr(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	noErr(t, err)
	return browserHTTPResult{status: response.StatusCode, header: response.Header.Clone(), body: string(body)}
}

func issuedTokenFromBody(t *testing.T, body string) string {
	t.Helper()
	const marker = "data-select-on-focus>"
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatal("credential response did not contain the one-time token field")
	}
	start += len(marker)
	end := strings.Index(body[start:], "</textarea>")
	if end < 0 {
		t.Fatal("credential response had an incomplete one-time token field")
	}
	token := html.UnescapeString(body[start : start+end])
	if token == "" || strings.ContainsAny(token, "\r\n<>") {
		t.Fatal("credential response contained an invalid one-time token")
	}
	return token
}

// openBrowser serves fixture and returns a browser that has loaded the
// repository page, which issues the general session and its CSRF cookie.
func openBrowser(t *testing.T, fixture apiFixture) (*httptest.Server, *http.Client, http.CookieJar) {
	t.Helper()
	server := serve(t, fixture.app.Handler())
	client, jar := newBrowserClient(t)
	if result := browserGET(t, client, server.URL+"/repositories/project"); result.status != http.StatusOK {
		t.Fatalf("repository status=%d", result.status)
	}
	return server, client, jar
}
