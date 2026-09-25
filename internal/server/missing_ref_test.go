package server

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"testing"
	"time"

	"owngit/internal/webui"
)

// QA-054: the overview and the Commits tab answer 404 for a branch or tag
// that does not exist, and a commit address answers 404 for a commit that
// is not in this repository, including one that exists in another
// repository. Each keeps the repository sidebar and a way back, like the
// Code tab (QA-045). An empty repository and a deleted default branch stay
// ordinary pages.
func TestMissingRefOrCommitAnswersNotFound(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, first := seedRepository(t, app, "hist", map[string]string{"README.md": "# History\n"}, when)
	second := commitFiles(t, work, map[string]string{"src/main.go": "package main\n"}, "second", when.Add(time.Hour))
	_, foreign := seedRepository(t, app, "other", map[string]string{"other.txt": "only here\n"}, when)
	goneWork, _ := seedRepository(t, app, "gone", map[string]string{"a.txt": "a\n"}, when)
	apiRunGit(t, goneWork, "push", "-q", "origin", "HEAD:refs/heads/dev")
	apiRunGit(t, goneWork, "push", "-q", "origin", ":refs/heads/main")
	if _, err := app.Repositories.Create(context.Background(), "blank", ""); err != nil {
		t.Fatal(err)
	}
	server := serve(t, app.Handler())
	client, _ := newBrowserClient(t)
	missing := strings.Repeat("0", 39) + "1"
	for _, lang := range []webui.Lang{webui.LangEN, webui.LangKO} {
		back := webui.Text(lang, webui.MsgBackToRepo)
		for _, test := range []struct {
			name, target string
			status       int
			want         []string
		}{
			{"the overview", "/repositories/hist", http.StatusOK, nil},
			{"the overview of a branch", "/repositories/hist?ref=refs%2Fheads%2Fmain", http.StatusOK, nil},
			{"the commits", "/repositories/hist/commits", http.StatusOK, nil},
			{"a commit", "/repositories/hist/commits/" + first, http.StatusOK, nil},
			{"a commit on a branch", "/repositories/hist/commits/" + second + "?ref=refs%2Fheads%2Fmain", http.StatusOK, nil},
			{"the overview of a missing branch", "/repositories/hist?ref=nope", http.StatusNotFound,
				[]string{webui.Text(lang, webui.MsgRepoRefMissing), back, `href="/repositories/hist"`}},
			{"the commits of a missing branch", "/repositories/hist/commits?ref=nope", http.StatusNotFound,
				[]string{webui.Text(lang, webui.MsgRepoRefMissing), back}},
			{"a missing commit", "/repositories/hist/commits/" + missing, http.StatusNotFound,
				[]string{webui.Text(lang, webui.MsgCommitNotFound), back}},
			{"a commit of another repository", "/repositories/hist/commits/" + foreign, http.StatusNotFound,
				[]string{webui.Text(lang, webui.MsgCommitNotFound), back}},
			{"a commit that is not an ID", "/repositories/hist/commits/not-a-commit", http.StatusNotFound,
				[]string{webui.Text(lang, webui.MsgCommitNotFound)}},
			{"a missing commit on a missing branch", "/repositories/hist/commits/" + missing + "?ref=nope", http.StatusNotFound, nil},
			{"an empty repository", "/repositories/blank", http.StatusOK, nil},
			{"the commits of an empty repository", "/repositories/blank/commits", http.StatusOK, nil},
			{"a deleted default branch", "/repositories/gone", http.StatusOK, []string{webui.Text(lang, webui.MsgRepoDefaultGone)}},
			{"the commits of a deleted default branch", "/repositories/gone/commits", http.StatusOK, nil},
		} {
			separator := "?"
			if strings.Contains(test.target, "?") {
				separator = "&"
			}
			page := browserGET(t, client, server.URL+test.target+separator+"lang="+string(lang))
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
			if strings.Contains(page.body, "only here") {
				t.Errorf("%s %s: shows another repository's content", lang, test.name)
			}
		}

		// Every link on these pages leads somewhere that answers: the tabs
		// never keep a ref that does not resolve, and "Commit history"
		// opens the default history.
		for _, test := range []struct {
			name, target string
			links        []string
			stale        []string
		}{
			{"a commit opened with a missing branch", "/repositories/hist/commits/" + first + "?ref=nope",
				[]string{`href="/repositories/hist"`, `href="/repositories/hist/code"`, `href="/repositories/hist/commits"`}, []string{"nope"}},
			{"a missing commit on a missing branch", "/repositories/hist/commits/" + missing + "?ref=nope",
				[]string{`<a class="btn" href="/repositories/hist/commits">`, `href="/repositories/hist/code"`}, []string{"nope"}},
			{"the overview of a missing branch", "/repositories/hist?ref=nope",
				[]string{`href="/repositories/hist/code"`, `href="/repositories/hist/commits"`}, []string{"nope"}},
			{"a deleted default branch", "/repositories/gone",
				[]string{`href="/repositories/gone/code"`, `href="/repositories/gone/commits"`}, []string{"refs%2Fheads%2Fmain"}},
		} {
			page := browserGET(t, client, server.URL+test.target+"&lang="+string(lang))
			if !strings.Contains(test.target, "?") {
				page = browserGET(t, client, server.URL+test.target+"?lang="+string(lang))
			}
			for _, link := range test.links {
				if !strings.Contains(page.body, link) {
					t.Errorf("%s %s: lacks %s", lang, test.name, link)
				}
			}
			// The language and appearance links reload this same address,
			// so they rightly keep it; every other link must not.
			for _, ref := range test.stale {
				for _, link := range regexp.MustCompile(`href="[^"]*[?&](amp;)?ref=`+regexp.QuoteMeta(ref)+`[&"]`).FindAllString(page.body, -1) {
					if !strings.Contains(link, "lang=") && !strings.Contains(link, "appearance=") {
						t.Errorf("%s %s: %s keeps the ref %s, which does not resolve", lang, test.name, link, ref)
					}
				}
			}
			for _, link := range test.links {
				target := strings.TrimSuffix(strings.TrimPrefix(link[strings.Index(link, "href=")+6:], ""), `">`)
				target = strings.TrimSuffix(target, `"`)
				if followed := browserGET(t, client, server.URL+target); followed.status != http.StatusOK {
					t.Errorf("%s %s: %s answers %d", lang, test.name, target, followed.status)
				}
			}
		}
	}
}

// A read that runs out of time while an operation holds the repository is a
// wait, not a missing commit: the page answers 503 before any 404.
func TestBusyRepositoryIsNotReportedAsMissing(t *testing.T) {
	app := newConfiguredApp(t)
	app.HTTPTimeout = 3 * time.Second
	seedRepository(t, app, "busy", map[string]string{"README.md": "# Busy\n"}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	get := func(target string) int {
		t.Helper()
		response, err := client.Get(server.URL + target)
		noErr(t, err)
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if status := get("/repositories/busy"); status != http.StatusOK {
		t.Fatalf("overview status=%d", status)
	}
	lock := app.Repositories.Locks.For("busy")
	lock.Lock()
	defer lock.Unlock()
	if status := get("/repositories/busy/commits/" + strings.Repeat("0", 39) + "1"); status != http.StatusServiceUnavailable {
		t.Errorf("a missing commit of a busy repository answered %d, want 503", status)
	}
}
