package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/importfetch"
	"owngit/internal/importsync"
	"owngit/internal/webui"
)

// unreadablePage is the notice of a repository page whose Git data could not
// be read.
const unreadablePage = "This repository&#39;s Git data could not be read."

// failGitWhile makes the app's Git fail, as a storage failure does (status
// 128), for any process with an argument matching the shell case pattern,
// while the returned file exists.
func failGitWhile(t *testing.T, app *App, pattern string) string {
	t.Helper()
	failPath := filepath.Join(t.TempDir(), "fail")
	useGitWrapper(t, app, `for a in "$@"; do case "$a" in `+pattern+`) if test -e `+serverShellQuote(failPath)+`; then echo 'fatal: simulated storage failure' >&2; exit 128; fi;; esac; done`)
	return failPath
}

// readFailureRepository creates repository name with a commit that has a
// README and a file in a folder, its parent, and annotated tags v1, v2 and
// v3 of the commit. It returns the ID of v1.
func readFailureRepository(t *testing.T, app *App, name string) (work, remote, commit, parent, tag string) {
	t.Helper()
	if _, err := app.Repositories.Create(context.Background(), name, ""); err != nil {
		t.Fatal(err)
	}
	remote, _ = app.Repositories.Path(name)
	work = filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Read Author")
	apiRunGit(t, work, "config", "user.email", "read@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("# Hello\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "first")
	parent = apiGitOutput(t, work, "rev-parse", "HEAD")
	noErr(t, os.MkdirAll(filepath.Join(work, "docs"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(work, "docs", "a.txt"), []byte("a\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "second")
	commit = apiGitOutput(t, work, "rev-parse", "HEAD")
	apiRunGit(t, work, "tag", "-a", "-m", "release", "v1")
	apiRunGit(t, work, "tag", "-a", "-m", "release", "v2")
	apiRunGit(t, work, "tag", "-a", "-m", "release", "v3")
	tag = apiGitOutput(t, work, "rev-parse", "refs/tags/v1")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main", "refs/tags/v1", "refs/tags/v2", "refs/tags/v3")
	return work, remote, commit, parent, tag
}

// A branch, tag, commit or file that does not exist is not found (404). A
// read that failed says nothing about it, so the Code, Commits, raw,
// archive and restore addresses answer that the repository cannot be read
// now (503) instead. Once Git works again the same addresses answer, so no
// failure was kept.
func TestRepositoryReadsTellMissingFromUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing Git wrapper is a Unix test fixture")
	}
	app := newConfiguredApp(t)
	_, _, commit, parent, tag := readFailureRepository(t, app, "reads")
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	client, _ := newBrowserClient(t)
	base := server.URL + "/repositories/reads"
	missing := strings.Repeat("e", len(commit))

	for _, path := range []string{
		"/code?ref=missing", "/code?ref=refs/heads/bad..name", "/code?path=nope", "/code?path=../escape",
		"/commits?ref=missing", "/commits/" + missing, "/commits/" + tag, "/commits/not-a-commit",
		"/raw?ref=refs/heads/main&path=nope", "/raw?ref=missing&path=README.md",
		"/archive?format=zip&ref=missing", "/archive?format=zip&ref=" + missing, "/archive?format=zip&ref=" + tag,
	} {
		if body, status := dashboardGET(t, client, base+path); status != http.StatusNotFound || strings.Contains(body, unreadablePage) {
			t.Errorf("GET %s status=%d unreadable=%v, want 404", path, status, strings.Contains(body, unreadablePage))
		}
	}
	if body, status := dashboardGET(t, client, base+"/restore?source="+missing+"&target=main"); status != http.StatusUnprocessableEntity || !strings.Contains(body, "That selection cannot be restored") {
		t.Errorf("restore of a missing commit status=%d, want 422 with the invalid selection", status)
	}

	// Each failing read below is the first of its kind, so the cache, which
	// the page after the recovery fills, cannot answer it. The order keeps
	// it so.
	for _, check := range []struct{ pattern, path string }{
		{"cat-file", "/archive?format=zip&ref=" + commit},
		{"--raw", "/restore?source=" + parent + "&target=main"},
		{"merge-base", "/commits/" + parent},
		{"--raw", "/commits/" + commit},
		{"diff-tree", "/commits/" + commit + "?path=docs/a.txt"},
		{"--max-count=100", "/commits"},
		{"ls-tree", "/code"},
		{"ls-tree", "/code?path=docs"},
		{"ls-tree", "/raw?ref=refs/heads/main&path=README.md"},
		{"blob", "/code?path=docs/a.txt"},
		{"cat-file", "/code?ref=refs/tags/v2"},
		{"cat-file", "/raw?ref=refs/tags/v3&path=README.md"},
		{"--max-count=8", ""},
	} {
		failPath := failGitWhile(t, app, check.pattern)
		noErr(t, os.WriteFile(failPath, nil, 0o600))
		body, status := dashboardGET(t, client, base+check.path)
		wantBody := unreadablePage
		if strings.HasPrefix(check.path, "/raw") || strings.HasPrefix(check.path, "/archive") {
			wantBody = webui.Text(webui.LangEN, webui.MsgErrUnavailable)
		}
		if status != http.StatusServiceUnavailable || !strings.Contains(body, wantBody) {
			t.Errorf("GET %s with failing %s status=%d, want 503 saying %q", check.path, check.pattern, status, wantBody)
		}
		noErr(t, os.Remove(failPath))
		if _, status := dashboardGET(t, client, base+check.path); status != http.StatusOK {
			t.Errorf("GET %s after Git recovered status=%d, want 200", check.path, status)
		}
	}
}

// A page whose Git data could not be read says so once, in either language.
// With no more specific reason known there is nothing else to add.
func TestUnreadableRepositoryPageStatesItOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing Git wrapper is a Unix test fixture")
	}
	app := newConfiguredApp(t)
	readFailureRepository(t, app, "once")
	failPath := failGitWhile(t, app, "--max-count=8")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	server := serve(t, app.Handler())
	client, _ := newBrowserClient(t)
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		body, status := dashboardGET(t, client, server.URL+"/repositories/once?lang="+string(lang))
		if shown := strings.Count(body, shownText(lang, webui.MsgRepoUnreadable)); status != http.StatusServiceUnavailable || shown != 1 {
			t.Errorf("%s unreadable page status=%d states it %d times, want 503 stating it once", lang, status, shown)
		}
	}
}

// Each read that fails is logged once with its cause where its answer is
// decided: the unavailable page, a panel that says it could not be read, and
// a raw file or archive download answered as unavailable. The branch and tag
// tips are two reads, so one Git failure behind both is two lines. A page
// that could be read logs nothing.
func TestRepositoryReadFailuresLogTheirCauseOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing Git wrapper is a Unix test fixture")
	}
	app := newConfiguredApp(t)
	_, _, commit, _, _ := readFailureRepository(t, app, "logged")
	server := serve(t, app.Handler())
	client, _ := newBrowserClient(t)
	base := server.URL + "/repositories/logged"
	serverLog := captureServerLog(t)

	// Each failing read is the first of its kind, so no cache answers it.
	for _, check := range []struct {
		pattern, path string
		status        int
		steps         []string
	}{
		{"--max-count=8", "", http.StatusServiceUnavailable, []string{"GET /repositories/logged: repository read"}},
		{"blob", "", http.StatusOK, []string{"GET /repositories/logged: README read"}},
		{"refs/owngit/retained", "", http.StatusOK, []string{"GET /repositories/logged: kept history read"}},
		{"--stdin", "", http.StatusOK, []string{"GET /repositories/logged: branch tip read", "GET /repositories/logged: tag tip read"}},
		{"ls-tree", "/raw?ref=refs/heads/main&path=docs/a.txt", http.StatusServiceUnavailable, []string{"GET /repositories/logged/raw: file read"}},
		{"cat-file", "/archive?format=zip&ref=" + commit, http.StatusServiceUnavailable, []string{"GET /repositories/logged/archive: archive ref read"}},
	} {
		since := len(serverLog.String())
		failPath := failGitWhile(t, app, check.pattern)
		noErr(t, os.WriteFile(failPath, nil, 0o600))
		_, status := dashboardGET(t, client, base+check.path)
		noErr(t, os.Remove(failPath))
		lines := loggedFailures(serverLog, since)
		if status != check.status || len(lines) != len(check.steps) {
			t.Errorf("GET %s with failing %s status=%d logged %d lines, want %d with %d:\n%s", check.path, check.pattern, status, len(lines), check.status, len(check.steps), strings.Join(lines, "\n"))
			continue
		}
		// The cause is the failed Git command and its exit status, quoted.
		for _, step := range check.steps {
			if logged := strings.Join(lines, "\n"); !strings.Contains(logged, step+` could not be completed: "git `) || !strings.Contains(logged, "exit status 128") {
				t.Errorf("GET %s with failing %s logged %q, want %q with its cause", check.path, check.pattern, lines, step)
			}
		}
	}

	since := len(serverLog.String())
	for _, path := range []string{"", "/raw?ref=refs/heads/main&path=docs/a.txt", "/archive?format=zip&ref=" + commit} {
		if _, status := dashboardGET(t, client, base+path); status != http.StatusOK {
			t.Errorf("GET %s after Git recovered status=%d, want 200", path, status)
		}
	}
	checkLoggedSteps(t, "pages that could be read", loggedFailures(serverLog, since))
}

// The overview's body, the recent commits and the top folder, is required:
// when it cannot be read, the page says the repository cannot be read. A side
// panel that cannot be read says so and keeps the rest of the page; it is
// never shown as empty, zero or absent.
func TestOverviewSidePanelsSayWhenTheyCouldNotBeRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing Git wrapper is a Unix test fixture")
	}
	app := newConfiguredApp(t)
	work, remote, commit, parent, _ := readFailureRepository(t, app, "panels")
	// A force push keeps the replaced commit as history.
	apiRunGit(t, work, "push", "--force", remote, parent+":refs/heads/main")
	apiRunGit(t, work, "push", remote, commit+":refs/heads/feature")
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	client, _ := newBrowserClient(t)
	overview := server.URL + "/repositories/panels"
	en := func(code webui.MessageCode) string { return webui.Text(webui.LangEN, code) }
	keptPanel := func(body string) string {
		start := strings.Index(body, `id="ov-kept-h"`)
		if start < 0 {
			return ""
		}
		panel := body[start:]
		return panel[:strings.Index(panel, "</section>")]
	}

	// The recent commits come first, before a page that reads them fills
	// the cache.
	failPath := failGitWhile(t, app, "--max-count=8")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	if body, status := dashboardGET(t, client, overview); status != http.StatusServiceUnavailable || !strings.Contains(body, unreadablePage) {
		t.Errorf("unreadable recent commits status=%d, want the unavailable page", status)
	}

	failPath = failGitWhile(t, app, "blob")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	body, status := dashboardGET(t, client, overview)
	if status != http.StatusOK || !strings.Contains(body, `>README.md</a>`) || !strings.Contains(body, en(webui.MsgReadmeUnreadable)) {
		t.Errorf("unreadable README status=%d named=%v note=%v", status, strings.Contains(body, `>README.md</a>`), strings.Contains(body, en(webui.MsgReadmeUnreadable)))
	}

	failPath = failGitWhile(t, app, "refs/owngit/retained")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	body, status = dashboardGET(t, client, overview)
	kept := keptPanel(body)
	if status != http.StatusOK || !strings.Contains(kept, en(webui.MsgRepoFactUnreadable)) || strings.Contains(kept, "item") {
		t.Errorf("unreadable kept history status=%d panel=%q, want the panel saying it could not be read, without a count", status, kept)
	}

	failPath = failGitWhile(t, app, "--stdin")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	body, status = dashboardGET(t, client, overview)
	// Each note carries its text once as the English attribute and once as
	// the shown text.
	if notes := strings.Count(body, ">"+en(webui.MsgRepoTipsUnreadable)+"<"); status != http.StatusOK || notes != 2 || strings.Contains(body, en(webui.MsgRepoNewestShown)) {
		t.Errorf("unreadable ref tips status=%d notes=%d newest_claimed=%v, want a note for branches and tags and no order claim",
			status, notes, strings.Contains(body, en(webui.MsgRepoNewestShown)))
	}
	noErr(t, os.Remove(failPath))

	body, status = dashboardGET(t, client, overview)
	kept = keptPanel(body)
	if status != http.StatusOK || !strings.Contains(kept, "1 item") || strings.Contains(kept, en(webui.MsgRepoFactUnreadable)) ||
		strings.Contains(body, en(webui.MsgRepoTipsUnreadable)) || !strings.Contains(body, en(webui.MsgRepoNewestShown)) ||
		strings.Contains(body, en(webui.MsgReadmeUnreadable)) || !strings.Contains(body, `>README.md</a>`) {
		t.Errorf("healthy overview status=%d kept=%q", status, kept)
	}
}

// Import history that could not be read is reported as such, beside the
// import status that could be read, never as no runs.
func TestImportTabSaysWhenHistoryCouldNotBeRead(t *testing.T) {
	fixture := newAPIFixture(t, false)
	fixture.app.Imports.Fetch = func(context.Context, importfetch.Request, importfetch.PackConsumer) (*importfetch.Result, error) {
		return nil, &importfetch.Error{Op: "connect", Kind: importfetch.ErrConnection}
	}
	if _, err := fixture.app.Imports.ConfigureSource(context.Background(), importsync.ConfigureInput{
		RepositoryID: "project", URL: "https://git.example.invalid/team/project.git", Mode: importsync.ModeStandalone,
	}); err != nil {
		t.Fatal(err)
	}
	// Three runs: the status reads the latest two, the history all of them.
	for range 3 {
		if _, err := fixture.app.Imports.Refresh(context.Background(), "project", fixture.app.importRunLimits()); err == nil {
			t.Fatal("the refresh did not fail")
		}
	}
	server := serve(t, fixture.app.Handler())
	client, _ := newBrowserClient(t)
	page := browserGET(t, client, server.URL+"/repositories/project/import")
	if page.status != http.StatusOK || strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgImportHistoryUnavailable)) {
		t.Fatalf("readable history status=%d unavailable=%v", page.status, strings.Contains(page.body, webui.Text(webui.LangEN, webui.MsgImportHistoryUnavailable)))
	}
	// The older run can no longer be read; the latest run still can.
	noErr(t, fixture.app.Store.Exec(context.Background(),
		`UPDATE import_runs SET started_at='unreadable' WHERE rowid=(SELECT MIN(rowid) FROM import_runs WHERE repository_id='project')`))
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		page = browserGET(t, client, server.URL+"/repositories/project/import?lang="+string(lang))
		if page.status != http.StatusOK || !strings.Contains(page.body, webui.Text(lang, webui.MsgImportHistoryUnavailable)) ||
			strings.Contains(page.body, webui.Text(lang, webui.MsgImportNoRun)) ||
			!strings.Contains(page.body, webui.Text(lang, webui.MsgImportLastRun)) || !strings.Contains(page.body, webui.Text(lang, webui.MsgImportErrorNetwork)) {
			t.Errorf("%s unreadable history status=%d unavailable=%v no_run=%v last_run=%v", lang, page.status,
				strings.Contains(page.body, webui.Text(lang, webui.MsgImportHistoryUnavailable)),
				strings.Contains(page.body, webui.Text(lang, webui.MsgImportNoRun)),
				strings.Contains(page.body, webui.Text(lang, webui.MsgImportLastRun)))
		}
	}
}

// A restore that failed for a reason other than a refusal cannot promise that
// the branch stayed as it was. When the page after it cannot be built
// either, the response still says the restore could not be confirmed,
// rather than only that the repository cannot be read.
func TestRestoreWithUnknownOutcomeIsReportedBeforeThePage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing Git wrapper is a Unix test fixture")
	}
	app := newConfiguredApp(t)
	_, remote, commit, parent, _ := readFailureRepository(t, app, "unknown-restore")
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	client, jar := newBrowserClient(t)
	if _, status := dashboardGET(t, client, server.URL+"/"); status != http.StatusOK {
		t.Fatalf("overview status=%d", status)
	}
	csrf := cookieValue(t, jar, server.URL, generalCookie)
	failPath := failGitWhile(t, app, "update-ref|--raw")
	noErr(t, os.WriteFile(failPath, nil, 0o600))
	body, status := restorePOST(t, client, server.URL+"/repositories/unknown-restore/restore", url.Values{
		"csrf": {csrf}, "source": {parent}, "target": {"main"}, "mode": {"all"},
		"expected_head": {commit}, "confirm": {"restore"},
	}, server.URL)
	unconfirmed := webui.Text(webui.LangEN, webui.MsgRestoreFailed)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, unconfirmed) {
		t.Fatalf("unknown restore outcome status=%d unconfirmed_message=%v", status, strings.Contains(body, unconfirmed))
	}
	if got := apiGitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main"); got != commit {
		t.Fatalf("the failed publication moved main to %s", got)
	}
}
