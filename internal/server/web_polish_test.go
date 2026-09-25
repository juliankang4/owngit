package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/webui"
)

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// pageCSRF reads the first form token on a page.
func pageCSRF(t *testing.T, body string) string {
	t.Helper()
	match := csrfField.FindStringSubmatch(body)
	if match == nil {
		t.Fatal("the page has no form token")
	}
	return match[1]
}

// afterAction gives the browser the notice cookie that the action producing
// notice sets on its redirect, for tests that check a notice's words without
// going through the action.
func afterAction(t *testing.T, jar http.CookieJar, serverURL, notice string) {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	noErr(t, err)
	jar.SetCookies(parsed, []*http.Cookie{{Name: noticeCookie, Value: notice, Path: "/"}})
}

// QA-045: the Code tab answers 404 for a path or a branch or tag that does
// not exist, keeps the repository sidebar, and links back.
func TestCodeTabAnswersNotFoundForAMissingPathOrRef(t *testing.T) {
	app := newConfiguredApp(t)
	seedRepository(t, app, "code404", map[string]string{"README.md": "# Code\n", "src/main.go": "package main\n"}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client, _ := newBrowserClient(t)
	base := server.URL + "/repositories/code404/code"
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		for _, test := range []struct {
			name, query string
			status      int
			want        []string
		}{
			{"an existing file", "?path=src%2Fmain.go", http.StatusOK, nil},
			{"an existing folder", "?path=src", http.StatusOK, nil},
			{"a missing file", "?path=nope.txt", http.StatusNotFound,
				[]string{webui.Text(lang, webui.MsgCodePathMissing), `href="/repositories/code404/code?ref=refs%2Fheads%2Fmain"`}},
			{"a missing folder", "?path=src%2Fnope%2Fdeeper", http.StatusNotFound, []string{webui.Text(lang, webui.MsgCodePathMissing)}},
			{"a missing branch", "?ref=refs%2Fheads%2Fnope", http.StatusNotFound,
				[]string{webui.Text(lang, webui.MsgRepoRefMissing), webui.Text(lang, webui.MsgBackToRepo), `href="/repositories/code404"`}},
			{"a missing branch and path", "?ref=nope&path=README.md", http.StatusNotFound, []string{webui.Text(lang, webui.MsgRepoRefMissing)}},
		} {
			page := browserGET(t, client, base+test.query+"&lang="+string(lang))
			if page.status != test.status {
				t.Errorf("%s %s: status %d, want %d", lang, test.name, page.status, test.status)
			}
			if !strings.Contains(page.body, `class="sb__repo"`) {
				t.Errorf("%s %s: the repository sidebar is missing", lang, test.name)
			}
			for _, want := range test.want {
				if !strings.Contains(page.body, want) {
					t.Errorf("%s %s: page lacks %q", lang, test.name, want)
				}
			}
		}
	}
}

// QA-046: a result notice appears only after the action that produced it,
// once. A crafted address with a notice shows nothing.
func TestResultNoticesNeedTheirAction(t *testing.T) {
	app := newConfiguredApp(t)
	server := serve(t, app.Handler())
	client, _ := newBrowserClient(t)
	for _, crafted := range []struct {
		path string
		code webui.MessageCode
	}{
		{"/?notice=restore_success", webui.MsgRestoreSuccess},
		{"/settings?notice=settings_saved", webui.MsgSettingsSaved},
		{"/?notice=repository_removed", webui.MsgRepoRemovedGeneric},
		{"/?notice=setup_completed", webui.MsgSetupCompleted},
	} {
		page := browserGET(t, client, server.URL+crafted.path)
		if strings.Contains(page.body, webui.Text(webui.LangEN, crafted.code)) {
			t.Errorf("%s shows its notice without the action", crafted.path)
		}
	}

	form := browserGET(t, client, server.URL+"/repositories/new")
	created := browserForm(t, client, server.URL+"/repositories", url.Values{"csrf": {pageCSRF(t, form.body)}, "name": {"noticed"}}, server.URL)
	location := created.header.Get("Location")
	if created.status != http.StatusSeeOther || !strings.Contains(location, "notice=repository_created") {
		t.Fatalf("create status=%d location=%q", created.status, location)
	}
	message := webui.Text(webui.LangEN, webui.MsgRepoCreated)
	if page := browserGET(t, client, server.URL+location); !strings.Contains(page.body, message) {
		t.Fatal("the created notice is not shown after creating")
	}
	if page := browserGET(t, client, server.URL+location); strings.Contains(page.body, message) {
		t.Fatal("the created notice is shown again on reload")
	}
	other, _ := newBrowserClient(t)
	if page := browserGET(t, other, server.URL+location); strings.Contains(page.body, message) {
		t.Fatal("another browser sees the created notice through the address")
	}
}

// QA-044: leaving shared access confirms it on the sign-in page at once, and
// the next sign-in does not repeat it.
func TestLeavingSharedAccessIsConfirmedOnTheSignInPage(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	accessHash, _ := auth.HashPassword("shared-password")
	adminHash, _ := auth.HashPassword("admin-password")
	noErr(t, store.CompleteSetup(context.Background(), repositoryRoot, "password", accessHash, adminHash, true))
	app.Repositories.SetRoot(repositoryRoot)
	server := serve(t, app.Handler())
	client, jar := newBrowserClient(t)
	signIn := func(next string) {
		t.Helper()
		browserGET(t, client, server.URL+"/login")
		login := browserForm(t, client, server.URL+"/login", url.Values{
			"csrf": {cookieValue(t, jar, server.URL, preauthCookie)}, "password": {"shared-password"}, "next": {next},
		}, server.URL)
		if login.status != http.StatusSeeOther {
			t.Fatalf("sign-in answered %d", login.status)
		}
	}
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		message := webui.Text(lang, webui.MsgLogoutDone)
		signIn("/")
		home := browserGET(t, client, server.URL+"/?lang="+string(lang))
		left := browserForm(t, client, server.URL+"/logout", url.Values{"csrf": {pageCSRF(t, home.body)}}, server.URL)
		location := left.header.Get("Location")
		if left.status != http.StatusSeeOther || !strings.HasPrefix(location, "/login") {
			t.Fatalf("%s leaving answered %d to %q", lang, left.status, location)
		}
		page := browserGET(t, client, server.URL+location)
		if page.status != http.StatusOK || !strings.Contains(page.body, message) {
			t.Fatalf("%s the sign-in page after leaving status=%d does not confirm it", lang, page.status)
		}
		signIn("/")
		if again := browserGET(t, client, server.URL+"/"); strings.Contains(again.body, message) {
			t.Errorf("%s the next sign-in repeats that shared access was left", lang)
		}
		// An old address that still carries the notice is not kept in the
		// sign-in return address.
		browserForm(t, client, server.URL+"/logout", url.Values{"csrf": {pageCSRF(t, browserGET(t, client, server.URL+"/").body)}}, server.URL)
		redirected := browserGET(t, client, server.URL+"/?notice=logout")
		if strings.Contains(redirected.header.Get("Location"), "notice") {
			t.Errorf("%s sign-in return address keeps the notice: %q", lang, redirected.header.Get("Location"))
		}
	}
}

// QA-046 for pull requests: a pull request result shows after its own
// action, once, and never from the address alone, even where the state
// would confirm it.
func TestPullRequestResultNoticesNeedTheirAction(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	csrf := url.Values{"csrf": {cookieValue(t, jar, server.URL, generalCookie)}}
	created := browserForm(t, client, server.URL+"/repositories/project/pull-requests", url.Values{
		"csrf": csrf["csrf"], "title": {"Noticed"}, "source_branch": {"feature"}, "target_branch": {"main"},
		"source_oid": {fixture.sourceOID}, "target_oid": {fixture.targetOID},
	}, server.URL)
	location := created.header.Get("Location")
	if created.status != http.StatusSeeOther || !strings.HasSuffix(location, "?notice=pull_request_created") {
		t.Fatalf("create status=%d location=%q body=%s", created.status, location, created.body)
	}
	message := webui.Text(webui.LangEN, webui.MsgPRCreated)
	if page := browserGET(t, client, server.URL+location); !strings.Contains(page.body, message) {
		t.Fatal("the created notice is not shown after creating")
	}
	if page := browserGET(t, client, server.URL+location); strings.Contains(page.body, message) {
		t.Fatal("the created notice is shown again on reload")
	}
	other, _ := newBrowserClient(t)
	browserGET(t, other, server.URL+"/repositories/project")
	if page := browserGET(t, other, server.URL+location); strings.Contains(page.body, message) {
		t.Fatal("another browser sees the created notice through the address")
	}
	for _, crafted := range []struct {
		key  string
		code webui.MessageCode
	}{{"pull_request_created", webui.MsgPRCreated}, {"pull_request_reopened", webui.MsgPRReopenedDone}} {
		if page := browserGET(t, client, server.URL+strings.TrimSuffix(location, "?notice=pull_request_created")+"?notice="+crafted.key); strings.Contains(page.body, webui.Text(webui.LangEN, crafted.code)) {
			t.Errorf("a crafted %s address on an open pull request shows its notice", crafted.key)
		}
	}
	closed := browserForm(t, client, server.URL+strings.TrimSuffix(location, "?notice=pull_request_created")+"/close", csrf, server.URL)
	closedMessage := webui.Text(webui.LangEN, webui.MsgPRClosedDone)
	if page := browserGET(t, client, server.URL+closed.header.Get("Location")); closed.status != http.StatusSeeOther || !strings.Contains(page.body, closedMessage) {
		t.Fatalf("close status=%d, notice shown after closing=%v", closed.status, strings.Contains(page.body, closedMessage))
	}
	if page := browserGET(t, client, server.URL+closed.header.Get("Location")); strings.Contains(page.body, closedMessage) {
		t.Fatal("the closed notice is shown again on reload")
	}
}
