package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/pullrequest"
	"owngit/internal/webui"
)

// Regressions for the pre-release web QA findings QA-008 and QA-010 to QA-016.

// QA-008: a second open pull request for one branch pair is refused in the
// browser and the API with the same code, and the browser links to the first.
func TestSecondPullRequestForABranchPairIsRefusedEverywhere(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	endpoint := server.URL + "/api/v1/repositories/project/pull-requests"
	first := apiRequest(t, http.MethodPost, endpoint, map[string]any{"title": "First", "source_branch": "feature", "target_branch": "main"}, "", "")
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first create status=%d", first.StatusCode)
	}
	first.Body.Close()

	second := apiRequest(t, http.MethodPost, endpoint, map[string]any{"title": "Second", "source_branch": "feature", "target_branch": "main"}, "", "")
	defer second.Body.Close()
	var envelope pullrequest.ErrorEnvelope
	noErr(t, json.NewDecoder(second.Body).Decode(&envelope))
	var details pullrequest.ExistingPullRequest
	noErr(t, json.Unmarshal(envelope.Error.Details, &details))
	if second.StatusCode != http.StatusConflict || envelope.Error.Code != "pull_request_exists" || details.Number != 1 {
		t.Fatalf("API second create status=%d error=%+v", second.StatusCode, envelope.Error)
	}

	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		result := browserForm(t, client, server.URL+"/repositories/project/pull-requests?lang="+string(lang), url.Values{
			"csrf": {cookieValue(t, jar, server.URL, generalCookie)}, "title": {"Browser duplicate"},
			"source_branch": {"feature"}, "target_branch": {"main"},
			"source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID},
		}, server.URL)
		if result.status != http.StatusConflict || !strings.Contains(result.body, webui.Text(lang, webui.MsgPRAlreadyOpen)) ||
			!strings.Contains(result.body, `href="/repositories/project/pull-requests/1">#1</a>`) {
			t.Fatalf("%s browser second create status=%d, want a refusal linking #1", lang, result.status)
		}
	}
	records, err := fixture.store.PullRequests(context.Background(), "project")
	if err != nil || len(records) != 1 {
		t.Fatalf("pull requests=%d err=%v, want only the first", len(records), err)
	}
}

// QA-008: merging a source the target already contains writes no commit, and
// the page says so in words rather than a raw mode value.
func TestBrowserMergeOfAContainedSourceIsAlreadyUpToDate(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	_, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{Repository: "project", Title: "Contained", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	apiRunGit(t, fixture.work, "push", "origin", "HEAD:refs/heads/main")

	merge := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/merge", url.Values{
		"csrf": {cookieValue(t, jar, server.URL, generalCookie)}, "source_oid": {fixture.sourceOID}, "target_oid": {fixture.sourceOID},
	}, server.URL)
	if merge.status != http.StatusSeeOther || merge.header.Get("Location") != "/repositories/project/pull-requests/1?notice=pull_request_up_to_date" {
		t.Fatalf("merge status=%d location=%q", merge.status, merge.header.Get("Location"))
	}
	if got := apiGitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main"); got != fixture.sourceOID {
		t.Fatalf("main=%s, want it unchanged at %s", got, fixture.sourceOID)
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		afterAction(t, jar, server.URL, "pull_request_up_to_date")
		page := browserGET(t, client, server.URL+merge.header.Get("Location")+"&lang="+string(lang))
		for _, code := range []webui.MessageCode{webui.MsgPRUpToDate, webui.MsgPRMergedUpToDate, webui.MsgPRMergedUpToDateFor, webui.MsgPRMergedTarget} {
			if !strings.Contains(page.body, webui.Text(lang, code)) {
				t.Errorf("%s: the merged page does not say %q", lang, webui.Text(lang, code))
			}
		}
		if strings.Contains(page.body, ">up_to_date<") || strings.Contains(page.body, webui.Text(lang, webui.MsgPRMerged)) {
			t.Errorf("%s: the merged page shows the raw mode or a plain merge notice", lang)
		}
	}
}

// QA-011: merge and review after the source branch was deleted explain the
// refusal on the page, and the API answers 409 rather than 500.
func TestActionsAfterTheSourceBranchWasDeletedExplainTheRefusal(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	_, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{Repository: "project", Title: "Deleted source", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	apiRunGit(t, fixture.work, "push", "origin", "--delete", "feature")

	form := url.Values{"csrf": {cookieValue(t, jar, server.URL, generalCookie)}, "source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID}}
	for _, action := range []string{"merge", "review/request", "review/skip"} {
		for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
			result := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/"+action+"?lang="+string(lang), form, server.URL)
			if result.status != http.StatusConflict || !strings.Contains(result.body, webui.Text(lang, webui.MsgMergeBlockedSourceGone)) {
				t.Errorf("%s %s: status=%d, want 409 with the missing-branch notice", action, lang, result.status)
			}
		}
	}
	response := apiRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/project/pull-requests/1/merge",
		pullrequest.RevisionInput{SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID}, "", "")
	if response.StatusCode != http.StatusConflict || apiErrorCode(t, response) != "source_branch_missing" {
		t.Fatalf("API merge status=%d", response.StatusCode)
	}
}

// QA-016: a notice from the address shows only when the pull request's state
// confirms it. Each address here comes with the notice cookie its action
// would set (QA-046), so the state check alone decides.
func TestPullRequestNoticesMustMatchTheState(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	_, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{Repository: "project", Title: "Open", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	merged := webui.Text(webui.LangEN, webui.MsgPRMerged)

	for _, target := range []string{
		"/repositories/project/pull-requests/1?notice=pull_request_merged",
		"/repositories/project/pull-requests/1?notice=pull_request_up_to_date",
		"/repositories/project/pull-requests/1?notice=review_requested",
		"/repositories/project/pull-requests/1?notice=review_skipped",
		"/settings?notice=pull_request_merged",
		"/?notice=pull_request_created",
	} {
		parsed, _ := url.Parse(target)
		afterAction(t, jar, server.URL, parsed.Query().Get("notice"))
		page := browserGET(t, client, server.URL+target)
		for _, code := range []webui.MessageCode{webui.MsgPRMerged, webui.MsgPRUpToDate, webui.MsgPRReviewAsked, webui.MsgPRReviewSkipped, webui.MsgPRCreated} {
			if strings.Contains(page.body, webui.Text(webui.LangEN, code)) {
				t.Errorf("%s shows %q, which the state does not confirm", target, webui.Text(webui.LangEN, code))
			}
		}
	}
	afterAction(t, jar, server.URL, "pull_request_created")
	if page := browserGET(t, client, server.URL+"/repositories/project/pull-requests/1?notice=pull_request_created"); !strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgPRCreated)) {
		t.Error("the created notice is missing on the open pull request")
	}
	_, err = fixture.app.PullRequests.Merge(context.Background(), "project", 1, pullrequest.RevisionInput{SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID})
	noErr(t, err)
	afterAction(t, jar, server.URL, "pull_request_merged")
	if page := browserGET(t, client, server.URL+"/repositories/project/pull-requests/1?notice=pull_request_merged"); !strings.Contains(page.body, merged) {
		t.Error("the merged notice is missing on the merged pull request")
	}
}

// QA-010: after a session ends, a form submission sends the reader back to
// the page that held the form, never to its POST-only address or off-site.
func TestLoginNextNeverPointsAtAPostOnlyAddress(t *testing.T) {
	for _, test := range []struct {
		name, method, target, referer, want string
	}{
		{"page read", http.MethodGet, "/repositories/alpha/code?ref=main", "", "/repositories/alpha/code?ref=main"},
		{"form on the same site", http.MethodPost, "/repositories", "http://owngit.test/repositories/new", "/repositories/new"},
		{"form keeps its query", http.MethodPost, "/repositories/alpha/pull-requests", "http://owngit.test/repositories/alpha/pull-requests/new?source=a&target=b", "/repositories/alpha/pull-requests/new?source=a&target=b"},
		{"no referer, repository setting", http.MethodPost, "/repositories/alpha/settings/default-branch", "", "/repositories/alpha/settings"},
		{"no referer, new repository", http.MethodPost, "/repositories", "", "/repositories/new"},
		{"other site", http.MethodPost, "/repositories/alpha/pull-requests/4/merge", "http://evil.test/repositories/alpha", "/repositories/alpha/pull-requests/4"},
		{"scheme-relative trick", http.MethodPost, "/repositories", "http://owngit.test//evil.test/x", "/"},
		{"form rendered on its own POST address", http.MethodPost, "/repositories/alpha/pull-requests/4/merge", "http://owngit.test/repositories/alpha/pull-requests/4/merge", "/repositories/alpha/pull-requests/4"},
		// Round 2 (F2): the page holding the form was itself shown at another
		// POST-only address, the refused merge.
		{"form on a refused merge page", http.MethodPost, "/repositories/a/pull-requests/1/review/request", "http://owngit.test/repositories/a/pull-requests/1/merge", "/repositories/a/pull-requests/1"},
		{"close from a refused review page", http.MethodPost, "/repositories/a/pull-requests/1/close", "http://owngit.test/repositories/a/pull-requests/1/review/skip", "/repositories/a/pull-requests/1"},
		{"refused default branch page", http.MethodPost, "/repositories/a/settings/default-branch", "http://owngit.test/repositories/a/settings/default-branch", "/repositories/a/settings"},
		{"restore preview page", http.MethodPost, "/repositories/a/restore", "http://owngit.test/repositories/a/restore/preview", "/repositories/a/restore"},
		{"refused new repository page", http.MethodPost, "/repositories", "http://owngit.test/repositories", "/repositories/new"},
		{"outside a repository", http.MethodPost, "/settings", "", "/settings"},
	} {
		request := httptest.NewRequest(test.method, "http://owngit.test"+test.target, nil)
		if test.referer != "" {
			request.Header.Set("Referer", test.referer)
		}
		if got := loginNext(request); got != test.want {
			t.Errorf("%s: next=%q, want %q", test.name, got, test.want)
		}
	}
}

func TestExpiredSessionFormSubmissionReturnsToTheForm(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	accessHash, _ := auth.HashPassword("shared-password")
	adminHash, _ := auth.HashPassword("admin-password")
	noErr(t, store.CompleteSetup(context.Background(), repositoryRoot, "password", accessHash, adminHash, true))
	app.Repositories.SetRoot(repositoryRoot)
	server := serve(t, app.Handler())
	client, _ := newBrowserClient(t)

	request, err := http.NewRequest(http.MethodPost, server.URL+"/repositories", strings.NewReader("name=late&csrf=stale"))
	noErr(t, err)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", server.URL)
	request.Header.Set("Referer", server.URL+"/repositories/new")
	result := browserRequest(t, client, request)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/login?next=%2Frepositories%2Fnew" {
		t.Fatalf("expired POST status=%d location=%q", result.status, result.header.Get("Location"))
	}
}

// QA-012 and QA-013: each password rule names itself, counts characters, and
// reads the same in English and Korean.
func TestSettingsPasswordRulesNameTheRuleThatFailed(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	accessHash, _ := auth.HashPassword("shared-password")
	adminHash, _ := auth.HashPassword("admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "password", accessHash, adminHash, false))
	app.Repositories.SetRoot(canonical)
	settings, _ := store.Settings(context.Background())
	noErr(t, store.CreateSession(context.Background(), "general-token", "general", "csrf-token", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: generalCookie, Value: "general-token", Path: "/"}})

	for _, test := range []struct {
		name, action, field, password string
		want                          webui.MessageCode
	}{
		{"shared equals administrator", webui.ActionChangeAccessPassword, "access_password", "admin-password", webui.MsgSetupGenSameAsAdmin},
		{"three Hangul characters", webui.ActionChangeAccessPassword, "access_password", "비밀번", webui.MsgSetupAccessPassShort},
		{"too many characters", webui.ActionChangeAccessPassword, "access_password", strings.Repeat("가", auth.MaximumPasswordCharacters+1), webui.MsgPasswordTooLong},
		{"administrator unchanged", webui.ActionChangeAdminPassword, "new_admin_password", "admin-password", webui.MsgSettingsAdminSame},
		{"administrator equals shared", webui.ActionChangeAdminPassword, "new_admin_password", "shared-password", webui.MsgSetupAdminSameAsGen},
		{"administrator too short", webui.ActionChangeAdminPassword, "new_admin_password", "관리자비번", webui.MsgSetupAdminShort},
	} {
		for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
			result := browserForm(t, client, server.URL+"/settings?lang="+string(lang), url.Values{
				"csrf": {"csrf-token"}, "action": {test.action}, "admin_password": {"admin-password"}, test.field: {test.password},
			}, server.URL)
			if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, webui.Text(lang, test.want)) {
				t.Errorf("%s (%s): status=%d, want %q", test.name, lang, result.status, webui.Text(lang, test.want))
			}
		}
	}
	// Eight Hangul characters meet the eight-character minimum.
	response := request(t, client, http.MethodPost, server.URL+"/settings", url.Values{
		"csrf": {"csrf-token"}, "action": {webui.ActionChangeAdminPassword}, "admin_password": {"admin-password"}, "new_admin_password": {"관리자비밀번호다"},
	}, server.URL)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("eight-character Hangul password status=%d", response.StatusCode)
	}
}

// QA-015: the appearance links work without JavaScript. The backend saves a
// valid choice in a preference cookie and renders it.
func TestAppearanceChoiceWorksWithoutJavaScript(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)

	page := browserGET(t, client, server.URL+"/repositories/project?appearance=dark")
	if !strings.Contains(page.body, `class="theme-dark" data-appearance="dark"`) || cookieValue(t, jar, server.URL, appearanceCookie) != "dark" {
		t.Fatalf("appearance=dark was not rendered and saved")
	}
	if !strings.Contains(page.body, `href="/repositories/project?appearance=light" data-appearance-set="light">`) ||
		!strings.Contains(page.body, `data-appearance-set="dark" aria-current="true"`) {
		t.Error("the appearance links do not mark the choice or keep the screen")
	}
	if strings.Contains(page.body, "lang=en&amp;appearance") || strings.Contains(page.body, "appearance=dark&amp;lang") {
		t.Error("the language links carry the saved appearance parameter")
	}
	later := browserGET(t, client, server.URL+"/repositories/project")
	if !strings.Contains(later.body, `class="theme-dark" data-appearance="dark"`) {
		t.Error("the saved appearance is not rendered on the next page")
	}
	invalid := browserGET(t, client, server.URL+"/repositories/project?appearance=sepia")
	if !strings.Contains(invalid.body, `class="theme-dark"`) || cookieValue(t, jar, server.URL, appearanceCookie) != "dark" {
		t.Error("an invalid appearance value replaced the saved choice")
	}
	browserGET(t, client, server.URL+"/repositories/project?appearance=system")
	if cookieValue(t, jar, server.URL, appearanceCookie) != "system" {
		t.Error("choosing System was not saved")
	}
}

// Close and reopen in the browser and the API, with the same access as merging
// and a refusal that links to the pull request holding the pair.
func TestCloseAndReopenPullRequestInBrowserAndAPI(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	_, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{Repository: "project", Title: "To close", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	csrf := url.Values{"csrf": {cookieValue(t, jar, server.URL, generalCookie)}}
	page := browserGET(t, client, server.URL+"/repositories/project/pull-requests/1")
	if !strings.Contains(page.body, `action="/repositories/project/pull-requests/1/close"`) || strings.Contains(page.body, "/reopen") {
		t.Fatal("an open pull request does not offer Close pull request")
	}
	if forged := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/close", url.Values{}, server.URL); forged.status != http.StatusForbidden {
		t.Fatalf("close without CSRF status=%d", forged.status)
	}
	closed := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/close", csrf, server.URL)
	if closed.status != http.StatusSeeOther || closed.header.Get("Location") != "/repositories/project/pull-requests/1?notice=pull_request_closed" {
		t.Fatalf("close status=%d location=%q", closed.status, closed.header.Get("Location"))
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		afterAction(t, jar, server.URL, "pull_request_closed")
		page = browserGET(t, client, server.URL+closed.header.Get("Location")+"&lang="+string(lang))
		for _, code := range []webui.MessageCode{webui.MsgPRClosedDone, webui.MsgPRStateClosed, webui.MsgPRClosedNote, webui.MsgPRReopen} {
			if !strings.Contains(page.body, webui.Text(lang, code)) {
				t.Errorf("%s: the closed pull request page does not say %q", lang, webui.Text(lang, code))
			}
		}
		if strings.Contains(page.body, `/merge"`) || strings.Contains(page.body, `/review/request"`) {
			t.Errorf("%s: a closed pull request still offers merge or review", lang)
		}
		list := browserGET(t, client, server.URL+"/repositories/project/pull-requests?lang="+string(lang))
		if !strings.Contains(list.body, webui.Text(lang, webui.MsgPRStateClosed)) || !strings.Contains(list.body, "To close") {
			t.Errorf("%s: the list does not keep the closed pull request", lang)
		}
	}
	merge := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/merge", url.Values{
		"csrf": csrf["csrf"], "source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID},
	}, server.URL)
	if merge.status != http.StatusConflict || apiGitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main") != fixture.targetOID {
		t.Fatalf("merge of a closed pull request status=%d", merge.status)
	}

	endpoint := server.URL + "/api/v1/repositories/project/pull-requests"
	second := apiRequest(t, http.MethodPost, endpoint, map[string]any{"title": "Replacement", "source_branch": "feature", "target_branch": "main"}, "", "")
	if second.StatusCode != http.StatusOK {
		t.Fatalf("create after close status=%d", second.StatusCode)
	}
	second.Body.Close()
	refused := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/reopen", csrf, server.URL)
	if refused.status != http.StatusConflict || !strings.Contains(refused.body, webui.Text(webui.LangEN, webui.MsgPRAlreadyOpen)) ||
		!strings.Contains(refused.body, `href="/repositories/project/pull-requests/2">#2</a>`) {
		t.Fatalf("reopen beside an open pull request status=%d", refused.status)
	}
	api := apiRequest(t, http.MethodPost, endpoint+"/1/reopen", map[string]any{}, "", "")
	if api.StatusCode != http.StatusConflict || apiErrorCode(t, api) != "pull_request_exists" {
		t.Fatalf("API reopen beside an open pull request status=%d", api.StatusCode)
	}
	if response := apiRequest(t, http.MethodPost, endpoint+"/2/close", nil, "", ""); response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("API close without a JSON body status=%d", response.StatusCode)
	}
	closedAPI := apiRequest(t, http.MethodPost, endpoint+"/2/close", map[string]any{}, "", "")
	if closedAPI.StatusCode != http.StatusOK || decodeAPISuccess(t, closedAPI).PullRequest.State != "closed" {
		t.Fatalf("API close status=%d", closedAPI.StatusCode)
	}
	reopened := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/reopen", csrf, server.URL)
	if reopened.status != http.StatusSeeOther || !strings.Contains(browserGET(t, client, server.URL+reopened.header.Get("Location")).body, webui.Text(webui.LangEN, webui.MsgPRReopenedDone)) {
		t.Fatalf("reopen status=%d", reopened.status)
	}
	_, err = fixture.app.PullRequests.Merge(context.Background(), "project", 1, pullrequest.RevisionInput{SourceOID: fixture.sourceOID, TargetOID: fixture.targetOID})
	noErr(t, err)
	fixed := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/close", csrf, server.URL)
	if fixed.status != http.StatusConflict || !strings.Contains(fixed.body, webui.Text(webui.LangEN, webui.MsgPRMergedFixed)) {
		t.Fatalf("close of a merged pull request status=%d", fixed.status)
	}
	if api := apiRequest(t, http.MethodPost, endpoint+"/1/close", map[string]any{}, "", ""); api.StatusCode != http.StatusConflict || apiErrorCode(t, api) != "pull_request_merged" {
		t.Fatalf("API close of a merged pull request status=%d", api.StatusCode)
	}
	page = browserGET(t, client, server.URL+"/repositories/project/pull-requests/1")
	if strings.Contains(page.body, "/close\"") || strings.Contains(page.body, "/reopen\"") {
		t.Error("a merged pull request offers close or reopen")
	}
}

// Closing and reopening are refused without the shared password, exactly
// like merging.
func TestCloseRequiresTheSameAccessAsMerge(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	_, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{Repository: "project", Title: "Protected", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	endpoint := server.URL + "/api/v1/repositories/project/pull-requests/1/close"
	if response := apiRequest(t, http.MethodPost, endpoint, map[string]any{}, "", ""); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("API close without the password status=%d", response.StatusCode)
	}
	client, _ := newBrowserClient(t)
	result := browserForm(t, client, server.URL+"/repositories/project/pull-requests/1/close", url.Values{"csrf": {"x"}}, server.URL)
	if result.status != http.StatusSeeOther || !strings.HasPrefix(result.header.Get("Location"), "/login?next=%2Frepositories%2Fproject%2Fpull-requests%2F1") {
		t.Fatalf("browser close without a session status=%d location=%q", result.status, result.header.Get("Location"))
	}
	if response := apiRequest(t, http.MethodPost, endpoint, map[string]any{}, "shared-password", ""); response.StatusCode != http.StatusOK {
		t.Fatalf("API close with the password status=%d", response.StatusCode)
	}
}

// F2: every address that accepts only POST maps to a page that answers GET.
func TestFormPagesAnswerGET(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, _ := openBrowser(t, fixture)
	_, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{Repository: "project", Title: "Form pages", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	for _, path := range []string{
		"/repositories",
		"/repositories/project/pull-requests/1/merge",
		"/repositories/project/pull-requests/1/close",
		"/repositories/project/pull-requests/1/reopen",
		"/repositories/project/pull-requests/1/review/request",
		"/repositories/project/pull-requests/1/review/skip",
		"/repositories/project/settings/default-branch",
		"/repositories/project/restore/preview",
		"/setup/redeem",
		"/logout",
		"/admin/logout",
		releaseDismissPath,
	} {
		page := formPage(path)
		if page == path {
			t.Errorf("%s is not mapped to a GET page", path)
			continue
		}
		result := browserGET(t, client, server.URL+page)
		if result.status == http.StatusNotFound || result.status == http.StatusMethodNotAllowed {
			t.Errorf("%s maps to %s, which answers %d", path, page, result.status)
		}
	}
}
