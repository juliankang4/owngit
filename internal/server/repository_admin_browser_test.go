package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
	"owngit/internal/state"
)

// Repository Settings tab, delete confirmation, and the overview figures that
// feed them. Every refusal below is checked against the store and the disk:
// the repository must still exist, whatever the backend's Delete would do.

const (
	adminTestSession = "repo-admin-session"
	adminTestCSRF    = "repo-admin-csrf"
)

func signInAdmin(t *testing.T, fixture apiFixture, server string, jar http.CookieJar) {
	t.Helper()
	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(context.Background(), adminTestSession, "admin", adminTestCSRF, settings.AdminSessionVersion, time.Now().Add(time.Hour)))
	parsed, _ := url.Parse(server)
	jar.SetCookies(parsed, []*http.Cookie{{Name: adminCookie, Value: adminTestSession, Path: "/"}})
}

func assertRepositoryIntact(t *testing.T, fixture apiFixture, context string) {
	t.Helper()
	if _, exists, err := fixture.store.Repository(t.Context(), "project"); err != nil || !exists {
		t.Fatalf("%s: repository record gone: exists=%v err=%v", context, exists, err)
	}
	if _, err := os.Stat(filepath.Join(fixture.remote, "HEAD")); err != nil {
		t.Fatalf("%s: Git folder changed: %v", context, err)
	}
}

// repositoryMenu returns the repository sidebar of a rendered page.
func repositoryMenu(t *testing.T, path, body string) string {
	t.Helper()
	at := strings.Index(body, `<nav class="sidebar"`)
	if at < 0 || !strings.Contains(body[at:], `class="sb__repo"`) {
		t.Fatalf("%s has no repository sidebar", path)
	}
	menu := body[at:]
	return menu[:strings.Index(menu, "</nav>")]
}

func TestRepositoryAdminTabsAreShownToEveryone(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	// The lock carries a tooltip, and its hidden words start with a space so
	// the spoken name reads "Settings (asks ...)", not "Settings(asks ...)".
	const lock = `<span class="adminlock" title="Asks for the administrator password"`
	paths := []string{"/repositories/project", "/repositories/project/code", "/repositories/project/commits",
		"/repositories/project/pull-requests", "/repositories/project/tasks"}

	check := func(viewer, path string, locked bool) {
		t.Helper()
		page := browserGET(t, client, server.URL+path)
		if page.status != http.StatusOK {
			t.Fatalf("%s %s status=%d", viewer, path, page.status)
		}
		strip := repositoryMenu(t, path, page.body)
		// Settings follows Import in the normal order; Delete comes last.
		importAt := strings.Index(strip, `href="/repositories/project/import"`)
		settingsAt := strings.Index(strip, `href="/repositories/project/settings"`)
		deleteAt := strings.Index(strip, `class="sb__item sb__item--danger" href="/repositories/project/delete"`)
		if importAt < 0 || settingsAt < importAt || deleteAt < settingsAt {
			t.Fatalf("%s %s section order import=%d settings=%d delete=%d:\n%s", viewer, path, importAt, settingsAt, deleteAt, strip)
		}
		// The danger control names itself in words, not only in colour.
		if !strings.Contains(strip[deleteAt:], "Delete repository") || !strings.Contains(strip[deleteAt:], "<svg") {
			t.Fatalf("%s %s delete control lacks its word or shape:\n%s", viewer, path, strip[deleteAt:])
		}
		// Without an administrator session both entries carry the lock and
		// the words that say the password is asked first; with one, neither.
		settings, remove := strip[settingsAt:deleteAt], strip[deleteAt:]
		for name, entry := range map[string]string{"settings": settings, "delete": remove} {
			marked := strings.Contains(entry, lock) && strings.Contains(entry, `<span class="visually-hidden"> <span data-en="(asks for the administrator password)"`)
			if marked != locked {
				t.Fatalf("%s %s %s entry lock=%v, want %v:\n%s", viewer, path, name, marked, locked, entry)
			}
		}
	}
	for _, path := range paths {
		check("visitor", path, true)
	}
	signInAdmin(t, fixture, server.URL, jar)
	for _, path := range append(paths, "/repositories/project/settings") {
		check("admin", path, false)
	}
}

func TestChecksPageOffersTheAdministratorLinksToEveryone(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	for _, viewer := range []string{"visitor", "admin"} {
		if viewer == "admin" {
			signInAdmin(t, fixture, server.URL, jar)
		}
		page := browserGET(t, client, server.URL+"/repositories/project/tasks")
		if page.status != http.StatusOK {
			t.Fatalf("%s tasks status=%d", viewer, page.status)
		}
		for _, link := range []string{"/repositories/project/helper-credentials", "/repositories/project/configured-checks"} {
			at := strings.Index(page.body, `<a href="`+link+`">`)
			if at < 0 {
				t.Fatalf("%s tasks page lacks the %s link", viewer, link)
			}
			entry := page.body[at : at+strings.Index(page.body[at:], "</a>")]
			if locked := strings.Contains(entry, `<span class="adminlock" `); locked != (viewer == "visitor") {
				t.Fatalf("%s %s link lock=%v:\n%s", viewer, link, locked, entry)
			}
		}
	}
}

// A login link can carry any next value. One with a control character is
// refused at the form and at the POST, because a browser drops tab, CR and
// LF inside a URL and "/\t/host" would otherwise redirect to another host.
func TestAdminLoginRefusesNextWithControlCharacters(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	for _, next := range []string{"/\t/example.invalid", "/\n/example.invalid", "/\x1b/example.invalid"} {
		login := browserGET(t, client, server.URL+"/admin/login?next="+url.QueryEscape(next))
		if login.status != http.StatusOK || !strings.Contains(login.body, `name="next" value="/"`) {
			t.Fatalf("login form for next=%q status=%d does not fall back to /", next, login.status)
		}
		result := browserForm(t, client, server.URL+"/admin/login", url.Values{
			"csrf": {cookieValue(t, jar, server.URL, preauthCookie)}, "admin_password": {"admin-password"}, "next": {next},
		}, server.URL)
		if result.status != http.StatusSeeOther || result.header.Get("Location") != "/" {
			t.Fatalf("login with next=%q status=%d location=%q", next, result.status, result.header.Get("Location"))
		}
	}
}

// Following an administrator entry point without a session opens the
// administrator login, and logging in returns to the entry that was followed.
func TestRepositoryAdminEntryReturnsAfterLogin(t *testing.T) {
	for _, path := range []string{"/repositories/project/settings", "/repositories/project/delete?lang=ko",
		"/repositories/project/configured-checks", "/repositories/project/helper-credentials", "/repositories/project/import"} {
		fixture := newAPIFixture(t, false)
		server, client, jar := openBrowser(t, fixture)
		entry := browserGET(t, client, server.URL+path)
		wantLogin := "/admin/login?next=" + url.QueryEscape(path)
		if entry.status != http.StatusSeeOther || entry.header.Get("Location") != wantLogin {
			t.Fatalf("GET %s status=%d location=%q, want %q", path, entry.status, entry.header.Get("Location"), wantLogin)
		}
		login := browserGET(t, client, server.URL+wantLogin)
		if login.status != http.StatusOK || !strings.Contains(login.body, `name="next" value="`+path+`"`) {
			t.Fatalf("admin login for %s status=%d does not carry next", path, login.status)
		}
		loginCSRF := cookieValue(t, jar, server.URL, preauthCookie)
		result := browserForm(t, client, server.URL+"/admin/login", url.Values{
			"csrf": {loginCSRF}, "admin_password": {"admin-password"}, "next": {path},
		}, server.URL)
		if result.status != http.StatusSeeOther || result.header.Get("Location") != path {
			t.Fatalf("admin login for %s status=%d location=%q", path, result.status, result.header.Get("Location"))
		}
		if back := browserGET(t, client, server.URL+path); back.status != http.StatusOK {
			t.Fatalf("GET %s after login status=%d", path, back.status)
		}
	}
}

func TestRepositoryAdminRoutesRequireAdministratorSession(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, _ := openBrowser(t, fixture)
	for _, path := range []string{"/repositories/project/settings", "/repositories/project/delete"} {
		result := browserGET(t, client, server.URL+path)
		if result.status != http.StatusSeeOther || result.header.Get("Location") != "/admin/login?next="+url.QueryEscape(path) {
			t.Fatalf("GET %s without admin status=%d location=%q", path, result.status, result.header.Get("Location"))
		}
	}
	for _, path := range []string{"/repositories/project/delete", "/repositories/project/settings/default-branch"} {
		result := browserForm(t, client, server.URL+path, url.Values{
			"csrf": {"anything"}, "mode": {"delete_files"}, "confirm_name": {"project"}, "admin_password": {"admin-password"}, "branch": {"feature"},
		}, server.URL)
		if result.status != http.StatusSeeOther || !strings.HasPrefix(result.header.Get("Location"), "/admin/login?next=") {
			t.Fatalf("POST %s without admin status=%d location=%q", path, result.status, result.header.Get("Location"))
		}
	}
	assertRepositoryIntact(t, fixture, "unauthenticated POST")
	summary, err := fixture.app.Repositories.Summary(t.Context(), "project")
	if err != nil || summary.DefaultBranch != "main" {
		t.Fatalf("unauthenticated POST changed the default branch: %q err=%v", summary.DefaultBranch, err)
	}
}

func TestRepositoryDeleteRefusalsNeverReachTheBackend(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	target := server.URL + "/repositories/project/delete"

	page := browserGET(t, client, target)
	if page.status != http.StatusOK || page.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("delete page status=%d cache=%q", page.status, page.header.Get("Cache-Control"))
	}
	if strings.Contains(page.body, " checked") {
		t.Fatal("the delete page preselects a consequence")
	}
	for _, want := range []string{`value="keep_files"`, `value="delete_files"`, `name="confirm_name"`, `name="admin_password"`, `name="csrf" value="` + adminTestCSRF + `"`, ".owngit-removed", "cannot be undone"} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("delete page lacks %q", want)
		}
	}

	valid := func() url.Values {
		return url.Values{"csrf": {adminTestCSRF}, "mode": {"delete_files"}, "confirm_name": {"project"}, "admin_password": {"admin-password"}}
	}
	cases := []struct {
		name    string
		edit    func(url.Values)
		origin  string
		status  int
		message string
	}{
		{name: "no origin", origin: "-", status: http.StatusForbidden},
		{name: "foreign origin", origin: "http://attacker.invalid", status: http.StatusForbidden},
		{name: "wrong csrf", edit: func(v url.Values) { v.Set("csrf", "wrong") }, status: http.StatusForbidden},
		{name: "no mode", edit: func(v url.Values) { v.Del("mode") }, status: http.StatusUnprocessableEntity, message: "Choose what happens to the Git files."},
		{name: "unknown mode", edit: func(v url.Values) { v.Set("mode", "purge") }, status: http.StatusUnprocessableEntity, message: "Choose what happens to the Git files."},
		{name: "wrong name", edit: func(v url.Values) { v.Set("confirm_name", "Project") }, status: http.StatusUnprocessableEntity, message: "The name does not match."},
		{name: "empty name", edit: func(v url.Values) { v.Set("confirm_name", "") }, status: http.StatusUnprocessableEntity, message: "The name does not match."},
		{name: "missing password", edit: func(v url.Values) { v.Del("admin_password") }, status: http.StatusUnauthorized, message: "That administrator password did not match."},
		{name: "wrong password", edit: func(v url.Values) { v.Set("admin_password", "not-it") }, status: http.StatusUnauthorized, message: "That administrator password did not match."},
	}
	for _, test := range cases {
		values := valid()
		if test.edit != nil {
			test.edit(values)
		}
		var result browserHTTPResult
		switch test.origin {
		case "":
			result = browserForm(t, client, target, values, server.URL)
		case "-":
			request, err := http.NewRequest(http.MethodPost, target, strings.NewReader(values.Encode()))
			noErr(t, err)
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			result = browserRequest(t, client, request)
		default:
			result = browserForm(t, client, target, values, test.origin)
		}
		if result.status != test.status {
			t.Fatalf("%s: status=%d want %d", test.name, result.status, test.status)
		}
		if test.message != "" && !strings.Contains(result.body, test.message) {
			t.Fatalf("%s: page does not explain the refusal", test.name)
		}
		if strings.Contains(result.body, "admin-password") || strings.Contains(result.body, "not-it") {
			t.Fatalf("%s: the password was echoed into the page", test.name)
		}
		if result.status == http.StatusUnprocessableEntity || result.status == http.StatusUnauthorized {
			// A valid choice survives the refusal; an invalid one is not guessed.
			keptMode := strings.Contains(result.body, `value="delete_files" required checked`)
			if wantKept := values.Get("mode") == "delete_files"; keptMode != wantKept {
				t.Fatalf("%s: mode kept=%v want %v", test.name, keptMode, wantKept)
			}
		}
		assertRepositoryIntact(t, fixture, test.name)
	}
}

func TestRepositoryDeleteWhileInUseIsRefusedAndKeepsTheChoice(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	// A Git request in progress holds the repository lock for reading. The
	// backend waits briefly for it and then reports the repository in use.
	lock := fixture.app.Repositories.Locks.For("project")
	lock.RLock()
	result := browserForm(t, client, server.URL+"/repositories/project/delete", url.Values{
		"csrf": {adminTestCSRF}, "mode": {"delete_files"}, "confirm_name": {"project"}, "admin_password": {"admin-password"},
	}, server.URL)
	lock.RUnlock()
	if result.status != http.StatusConflict || !strings.Contains(result.body, "Another Git operation, such as a push or a clone, is using the repository.") {
		t.Fatalf("delete while in use status=%d body=%s", result.status, result.body)
	}
	if !strings.Contains(result.body, `value="delete_files" required checked`) {
		t.Fatal("the chosen mode was lost after the repository was busy")
	}
	if strings.Contains(result.body, "admin-password") {
		t.Fatal("the password was echoed back")
	}
	if cookieNamed(result.header, removedCookie) {
		t.Fatal("a refused deletion set the removal notice")
	}
	assertRepositoryIntact(t, fixture, "delete while in use")
}

func TestRepositoryDeleteKeepFilesNamesTheKeptFolder(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	result := browserForm(t, client, server.URL+"/repositories/project/delete", url.Values{
		"csrf": {adminTestCSRF}, "mode": {"keep_files"}, "confirm_name": {"project"}, "admin_password": {"admin-password"},
	}, server.URL)
	if result.status != http.StatusSeeOther || result.header.Get("Location") != "/?notice="+removedNotice {
		t.Fatalf("keep-files delete status=%d location=%q body=%s", result.status, result.header.Get("Location"), result.body)
	}
	if _, exists, err := fixture.store.Repository(t.Context(), "project"); err != nil || exists {
		t.Fatalf("repository record remains: exists=%v err=%v", exists, err)
	}
	dashboard := browserGET(t, client, server.URL+"/?notice="+removedNotice)
	if !strings.Contains(dashboard.body, "push &#39;"+server.URL+"/git/project.git&#39; &#39;refs/heads/*:refs/heads/*&#39;") {
		t.Fatalf("dashboard lacks the recovery command:\n%s", dashboard.body)
	}
	if !strings.Contains(dashboard.body, "Removed from OwnGit, files kept:") || !strings.Contains(dashboard.body, ">project<") ||
		!strings.Contains(dashboard.body, removedFolderName) {
		t.Fatalf("dashboard does not name the repository and the kept folder:\n%s", dashboard.body)
	}
	again := browserGET(t, client, server.URL+"/?notice="+removedNotice)
	if strings.Contains(again.body, removedFolderName) {
		t.Fatal("the removal notice was shown twice")
	}
}

func TestRepositoryDeleteFilesRemovesTheFolder(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	result := browserForm(t, client, server.URL+"/repositories/project/delete", url.Values{
		"csrf": {adminTestCSRF}, "mode": {"delete_files"}, "confirm_name": {"project"}, "admin_password": {"admin-password"},
	}, server.URL)
	if result.status != http.StatusSeeOther {
		t.Fatalf("delete-files status=%d body=%s", result.status, result.body)
	}
	if _, err := os.Stat(fixture.remote); !os.IsNotExist(err) {
		t.Fatalf("Git folder remains after delete_files: %v", err)
	}
	dashboard := browserGET(t, client, server.URL+"/?notice="+removedNotice)
	if !strings.Contains(dashboard.body, "Deleted with its files:") {
		t.Fatal("dashboard does not report the deletion")
	}
}

func TestRemovalNoticeTrustsOnlyItsOwnCookie(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	plain := browserGET(t, client, server.URL+"/?notice="+removedNotice)
	if !strings.Contains(plain.body, "The repository was removed.") || strings.Contains(plain.body, removedFolderName) {
		t.Fatal("a crafted notice link claimed more than the generic sentence")
	}
	parsed, _ := url.Parse(server.URL)
	jar.SetCookies(parsed, []*http.Cookie{{Name: removedCookie, Value: "%%%not-base64", Path: "/"}})
	malformed := browserGET(t, client, server.URL+"/?notice="+removedNotice)
	if !strings.Contains(malformed.body, "The repository was removed.") {
		t.Fatal("a malformed removal cookie was not answered with the generic notice")
	}
	value := base64.RawURLEncoding.EncodeToString([]byte(`{"n":"<b>x</b>","m":"keep_files","k":"/srv/.owngit-removed/x"}`))
	// The details name storage paths, so a viewer without an administrator
	// session gets only the generic sentence, even with a well-formed cookie.
	jar.SetCookies(parsed, []*http.Cookie{{Name: removedCookie, Value: value, Path: "/"}})
	visitor := browserGET(t, client, server.URL+"/?notice="+removedNotice)
	if !strings.Contains(visitor.body, "The repository was removed.") || strings.Contains(visitor.body, "/srv/.owngit-removed/x") ||
		strings.Contains(visitor.body, "&lt;b&gt;x&lt;/b&gt;") {
		t.Fatal("a removal notice showed its details to a viewer without an administrator session")
	}
	cleared := false
	for _, line := range visitor.header.Values("Set-Cookie") {
		cleared = cleared || (strings.HasPrefix(line, removedCookie+"=;") && strings.Contains(line, "Max-Age=0"))
	}
	if !cleared {
		t.Fatal("the removal cookie was not cleared for a viewer without an administrator session")
	}
	signInAdmin(t, fixture, server.URL, jar)
	jar.SetCookies(parsed, []*http.Cookie{{Name: removedCookie, Value: value, Path: "/"}})
	escaped := browserGET(t, client, server.URL+"/?notice="+removedNotice)
	if strings.Contains(escaped.body, "<b>x</b>") || !strings.Contains(escaped.body, "&lt;b&gt;x&lt;/b&gt;") {
		t.Fatal("the removal notice did not escape its detail")
	}
	if strings.Contains(escaped.body, "git --git-dir") {
		t.Fatal("a removal cookie without a valid repository ID produced a recovery command")
	}
}

func TestSetDefaultBranchAcceptsOnlyExistingBranches(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)

	settings := browserGET(t, client, server.URL+"/repositories/project/settings")
	if settings.status != http.StatusOK {
		t.Fatalf("settings status=%d", settings.status)
	}
	for _, want := range []string{`<option value="main" selected>main</option>`, `<option value="feature">feature</option>`,
		`href="/repositories/project/configured-checks"`, `href="/repositories/project/runner-tokens"`,
		`href="/repositories/project/helper-credentials"`, `href="/repositories/project/import"`, `href="/repositories/project/delete"`} {
		if !strings.Contains(settings.body, want) {
			t.Fatalf("settings page lacks %q", want)
		}
	}

	target := server.URL + "/repositories/project/settings/default-branch"
	wrongCSRF := browserForm(t, client, target, url.Values{"csrf": {"wrong"}, "branch": {"feature"}}, server.URL)
	if wrongCSRF.status != http.StatusForbidden {
		t.Fatalf("wrong csrf status=%d", wrongCSRF.status)
	}
	for _, branch := range []string{"", "nope", "refs/heads/feature", "../main"} {
		result := browserForm(t, client, target, url.Values{"csrf": {adminTestCSRF}, "branch": {branch}}, server.URL)
		if result.status != http.StatusUnprocessableEntity || !strings.Contains(result.body, "That branch does not exist.") {
			t.Fatalf("branch %q status=%d", branch, result.status)
		}
	}
	summary, err := fixture.app.Repositories.Summary(t.Context(), "project")
	if err != nil || summary.DefaultBranch != "main" {
		t.Fatalf("a refused branch changed HEAD: %q err=%v", summary.DefaultBranch, err)
	}

	accepted := browserForm(t, client, target, url.Values{"csrf": {adminTestCSRF}, "branch": {"feature"}}, server.URL)
	if accepted.status != http.StatusSeeOther || accepted.header.Get("Location") != "/repositories/project/settings?notice="+defaultBranchNotice {
		t.Fatalf("default branch change status=%d location=%q", accepted.status, accepted.header.Get("Location"))
	}
	summary, err = fixture.app.Repositories.Summary(t.Context(), "project")
	if err != nil || summary.DefaultBranch != "feature" {
		t.Fatalf("default branch=%q err=%v", summary.DefaultBranch, err)
	}
}

func TestRepositoryOverviewListsNewestRefsWithinBounds(t *testing.T) {
	fixture := newAPIFixture(t, false)
	work := fixture.work
	apiRunGit(t, work, "checkout", "main")
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// Ten commits, a branch and a tag at each, dated one day apart. Tag names
	// run against the dates, so alphabetical and newest-first orders differ.
	for index := 0; index < 10; index++ {
		noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte(fmt.Sprintf("line %d\n", index)), 0o600))
		apiRunGit(t, work, "add", ".")
		date := base.AddDate(0, 0, index).Format(time.RFC3339)
		apiRunGit(t, work, "commit", "-m", fmt.Sprintf("step %d", index), "--date="+date)
		apiRunGit(t, work, "branch", fmt.Sprintf("b-%02d", 9-index))
		apiRunGit(t, work, "tag", fmt.Sprintf("v0.%d", 9-index))
	}
	apiRunGit(t, work, "push", "origin", "HEAD:refs/heads/main", "refs/heads/b-*:refs/heads/b-*", "refs/tags/*:refs/tags/*")
	_, err := fixture.store.CreatePullRequest(t.Context(), "project", "Open one", "feature", "main",
		fixture.sourceOID, fixture.targetOID, state.ReviewNotRequested, time.Now())
	noErr(t, err)

	server, client, _ := openBrowser(t, fixture)
	page := browserGET(t, client, server.URL+"/repositories/project")
	if page.status != http.StatusOK {
		t.Fatalf("overview status=%d", page.status)
	}
	tags := pageSection(t, page.body, "Tags")
	names := regexp.MustCompile(`refs%2Ftags%2F(v0\.\d)`).FindAllStringSubmatch(tags, -1)
	var order []string
	for _, match := range names {
		order = append(order, match[1])
	}
	if strings.Join(order, ",") != "v0.0,v0.1,v0.2,v0.3,v0.4" {
		t.Fatalf("tag rows=%v, want the five newest, newest first", order)
	}
	if !strings.Contains(tags, "10 tags") || !strings.Contains(tags, "refs=all#refs") {
		t.Fatal("the tag list hides rows without saying how many exist or offering all")
	}
	branches := pageSection(t, page.body, "Branches")
	if count := strings.Count(branches, `class="refline"`); count != overviewBranchRows {
		t.Fatalf("branch rows=%d, want %d", count, overviewBranchRows)
	}
	if first := strings.Index(branches, "refs%2Fheads%2Fmain"); first < 0 || first > strings.Index(branches, "refs%2Fheads%2Fb-") {
		t.Fatal("the default branch is not listed first")
	}
	commits := pageSection(t, page.body, "Recent commits")
	if count := strings.Count(commits, `class="row row--commit"`); count != overviewRecentCommits {
		t.Fatalf("recent commit rows=%d, want %d", count, overviewRecentCommits)
	}
	if !strings.Contains(page.body, `href="/repositories/project/pull-requests">1</a>`) {
		t.Fatal("the summary does not link the open pull request count")
	}
	if !strings.Contains(page.body, "No check has run for this revision") {
		t.Fatal("the summary does not say that no check ran on the default branch tip")
	}

	all := browserGET(t, client, server.URL+"/repositories/project?refs=all")
	if count := strings.Count(pageSection(t, all.body, "Tags"), `class="refline"`); count != 10 {
		t.Fatalf("show all listed %d tags, want 10", count)
	}
	if !strings.Contains(all.body, `href="/repositories/project#refs"`) {
		t.Fatal("the full list offers no way back to the newest few")
	}
}

func cookieNamed(header http.Header, name string) bool {
	for _, line := range header.Values("Set-Cookie") {
		if strings.HasPrefix(line, name+"=") {
			return true
		}
	}
	return false
}

func TestBusyReasonsGetTheirOwnSentence(t *testing.T) {
	for err, want := range map[error]string{
		repository.ErrImportRunning:                            "repoadmin.busy.import",
		repository.ErrCheckRunning:                             "repoadmin.busy.check",
		repository.ErrCheckCleanupPending:                      "repoadmin.busy.check_cleanup",
		repository.ErrRepositoryInUse:                          "repoadmin.busy.in_use",
		repository.ErrRepositoryBusy:                           "repoadmin.busy",
		fmt.Errorf("wrapped: %w", repository.ErrImportRunning): "repoadmin.busy.import",
	} {
		if got, ok := busyNotice(err); !ok || string(got) != want {
			t.Errorf("%v: notice=%q ok=%v, want %q", err, got, ok, want)
		}
	}
	for _, err := range []error{repository.ErrRepositoryNotFound, repository.ErrDeleteIncomplete, fmt.Errorf("disk full")} {
		if _, ok := busyNotice(err); ok {
			t.Errorf("%v was reported as busy", err)
		}
	}
}

func TestIncompleteDeletionIsReportedAsRemovedWithCleanupOwed(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	parsed, _ := url.Parse(server.URL)
	value := base64.RawURLEncoding.EncodeToString([]byte(`{"n":"old-project","m":"delete_files","i":true}`))
	jar.SetCookies(parsed, []*http.Cookie{{Name: removedCookie, Value: value, Path: "/"}})
	page := browserGET(t, client, server.URL+"/?notice="+removedNotice)
	for _, want := range []string{"Removed from OwnGit, but its files are not fully moved or deleted yet:", ">old-project<", "finishes at the next start"} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("incomplete deletion notice lacks %q", want)
		}
	}
	if strings.Contains(page.body, "Deleted with its files:") {
		t.Fatal("an incomplete deletion was reported as finished")
	}
}

func TestKeptNoticeGivesAQuotedRecoveryCommand(t *testing.T) {
	quoted := recoveryCommand("/srv/it's here/.owngit-removed/old-project-20260923T120000Z.git", "http://host:7654/git/old-project.git")
	if quoted != `git --git-dir '/srv/it'\''s here/.owngit-removed/old-project-20260923T120000Z.git' push 'http://host:7654/git/old-project.git' 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'` {
		t.Fatalf("recovery command is not quoted for a POSIX shell: %s", quoted)
	}
	if strings.Contains(quoted, "--mirror") {
		t.Fatal("the recovery command uses --mirror, which OwnGit refuses for refs other than branches and tags")
	}

	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	parsed, _ := url.Parse(server.URL)
	gitPath, err := fixture.app.Repositories.Path("old-project")
	noErr(t, err)
	removed := filepath.Join(filepath.Dir(gitPath), removedFolderName)
	notice := func(id, kept string) string {
		t.Helper()
		value := base64.RawURLEncoding.EncodeToString([]byte(`{"n":"old-project","d":` + fmt.Sprintf("%q", id) + `,"m":"keep_files","k":` + fmt.Sprintf("%q", kept) + `}`))
		jar.SetCookies(parsed, []*http.Cookie{{Name: removedCookie, Value: value, Path: "/"}})
		return browserGET(t, client, server.URL+"/?notice="+removedNotice).body
	}

	for _, kept := range []string{
		filepath.Join(removed, "old-project-20260923T120000Z.git"),
		filepath.Join(removed, "old-project-20260923T120000Z-2.git"),
	} {
		body := notice("old-project", kept)
		want := "push &#39;" + server.URL + "/git/old-project.git&#39; &#39;refs/heads/*:refs/heads/*&#39; &#39;refs/tags/*:refs/tags/*&#39;"
		if !strings.Contains(body, "create a new repository with the same name, then run:") || !strings.Contains(body, want) ||
			!strings.Contains(body, "Run this on the computer where OwnGit runs") {
			t.Fatalf("kept notice for %s lacks the recovery command:\n%s", kept, body)
		}
	}

	// A cookie the backend did not write gets the sentence, never a command.
	for name, test := range map[string]struct{ id, kept string }{
		"invalid id":         {"../x", filepath.Join(removed, "old-project-20260923T120000Z.git")},
		"another repository": {"other", filepath.Join(removed, "old-project-20260923T120000Z.git")},
		"outside storage":    {"old-project", "/srv/.owngit-removed/old-project-20260923T120000Z.git"},
		"nested":             {"old-project", filepath.Join(removed, "x", "old-project-20260923T120000Z.git")},
		"unclean":            {"old-project", removed + "/../" + removedFolderName + "/old-project-20260923T120000Z.git"},
		"not a stamp":        {"old-project", filepath.Join(removed, "old-project-latest.git")},
		"extra suffix":       {"old-project", filepath.Join(removed, "old-project-20260923T120000Z.git.bak")},
	} {
		body := notice(test.id, test.kept)
		if strings.Contains(body, "git --git-dir") || !strings.Contains(body, "To bring it back, create a new repository and push from that folder.") {
			t.Errorf("%s: a kept path of the wrong shape was offered a command", name)
		}
	}
}
