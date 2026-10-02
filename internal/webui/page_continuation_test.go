package webui

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestUnavailableFileDrawerDoesNotPresentACompleteListing(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range []Lang{LangEN, LangKO} {
		page := codePage(lang, &FileView{Path: "small.txt", Lines: []string{"x"}})
		page.Code.ListingUnavailable = true
		page.Code.Entries = []TreeEntry{{Name: "should-not-be-listed.txt", Path: "should-not-be-listed.txt", URL: "/hidden"}}
		page.Code.Continuation = PageContinuation{First: 1, Last: 1, Total: 2, MoreURL: "/hidden-more"}
		out := render(t, r, page)
		start := strings.Index(out, `<nav class="tree"`)
		if start < 0 {
			t.Fatal("drawer missing")
		}
		end := strings.Index(out[start:], "</nav>")
		if end < 0 {
			t.Fatal("drawer did not close")
		}
		drawer := out[start : start+end]
		message := "This is temporarily unavailable."
		if lang == LangKO {
			message = "지금은 사용할 수 없습니다."
		}
		if !strings.Contains(drawer, message) || strings.Contains(drawer, "tree__row") || strings.Contains(drawer, "data-page-continuation") || strings.Contains(drawer, "hidden") || strings.Contains(drawer, "page-transfer-complete") {
			t.Fatalf("%s: unavailable drawer presents listing data: %s", lang, drawer)
		}
		if !strings.Contains(out, `class="codetable__t">x</td>`) || !strings.Contains(out, `<summary class="btn drawer__btn">`) || strings.Count(out, "data-page-transfer-complete") != 1 {
			t.Fatalf("%s: file body, native toggle or document tail missing", lang)
		}
	}
}

// pageLinks returns every rendered page-link group and where each starts.
func pageLinks(out string) (groups []string, starts []int) {
	from := 0
	for {
		start := strings.Index(out[from:], `<nav class="pager pager--pages"`)
		if start < 0 {
			return groups, starts
		}
		start += from
		end := start + strings.Index(out[start:], "</nav>") + len("</nav>")
		groups, starts = append(groups, out[start:end]), append(starts, start)
		from = end
	}
}

func TestLongContentHasTheSamePageLinksAboveAndBelow(t *testing.T) {
	r := newRenderer(t)
	// Each view renders its content with the given continuation; the marker
	// is the class of every content row, so the copy below follows the last.
	views := []struct {
		name, row string
		page      func(Lang, PageContinuation) RepositoryPage
	}{
		{"file", `class="codetable__t"`, func(lang Lang, c PageContinuation) RepositoryPage {
			return codePage(lang, &FileView{Path: "lines.txt", Lines: []string{"a", "b"}, FirstLine: 10001, Continuation: c})
		}},
		{"diff", `class="difftable__t"`, func(lang Lang, c PageContinuation) RepositoryPage {
			page := repoPage(fullChrome(lang), RepoTabCommits)
			file := DiffFile{Path: "lines.txt", Status: "modified", Additions: 2, Selected: true, Hunks: []DiffHunk{{Header: "@@ -0,0 +10001,2 @@", Lines: []DiffLine{{Kind: "add", Text: "a"}, {Kind: "add", Text: "b"}}}}}
			page.Commits.Detail = &CommitDetail{Commit: CommitSummary{Subject: "Add lines"}, Files: []DiffFile{file}, SelectedPath: "lines.txt", Continuation: c}
			return page
		}},
		{"folder", `class="flist__row"`, func(lang Lang, c PageContinuation) RepositoryPage {
			page := codePage(lang, nil)
			page.Code.Entries = []TreeEntry{{Name: "a.txt", Path: "a.txt", Kind: "file", URL: "/a"}, {Name: "b.txt", Path: "b.txt", Kind: "file", URL: "/b"}}
			page.Code.Continuation = c
			return page
		}},
	}
	middle := PageContinuation{First: 10001, Last: 20000, Total: 30000, FirstURL: "/first", MoreURL: "/next"}
	last := PageContinuation{First: 20001, Last: 30000, Total: 30000, FirstURL: "/first"}
	cut := PageContinuation{First: 1, Last: 10000, Total: 12345, MoreURL: "/next", Incomplete: true}
	for _, lang := range []Lang{LangEN, LangKO} {
		for _, view := range views {
			for _, c := range []PageContinuation{middle, last, cut} {
				out := render(t, r, view.page(lang, c))
				groups, starts := pageLinks(out)
				if len(groups) != 2 || groups[0] != groups[1] {
					t.Fatalf("%s (%s) %+v: want two identical page-link groups, got %d", view.name, lang, c, len(groups))
				}
				if first := strings.Index(out, view.row); first < 0 || starts[0] > first || starts[1] < strings.LastIndex(out, view.row) {
					t.Errorf("%s (%s): page links are not above and below the content", view.name, lang)
				}
				group := groups[0]
				if strings.Contains(group, " id=") || !strings.Contains(group, `aria-label="`+wantText(lang, MsgCodePages)+`"`) {
					t.Errorf("%s (%s): page links repeat an id or lack their name: %s", view.name, lang, group)
				}
				if (c.MoreURL != "") != strings.Contains(group, `<a class="btn" href="/next" rel="next">`) || (c.FirstURL != "") != strings.Contains(group, `<a class="btn" href="/first">`) {
					t.Errorf("%s (%s): page links do not match the continuation: %s", view.name, lang, group)
				}
			}
			// A page that fits shows no page links, above or below.
			out := render(t, r, view.page(lang, PageContinuation{First: 1, Last: 2, Total: 2}))
			if groups, _ := pageLinks(out); len(groups) != 0 || strings.Contains(out, "data-page-continuation") {
				t.Errorf("%s (%s): a single page shows page links", view.name, lang)
			}
		}
		// The file drawer's list scrolls in its own box, so it has one group
		// above its rows; the file below it has none of its own.
		page := codePage(lang, &FileView{Path: "a.txt", Lines: []string{"a"}})
		page.Code.Entries = []TreeEntry{{Name: "a.txt", Path: "a.txt", Kind: "file", URL: "/a"}}
		page.Code.Continuation = middle
		out := render(t, r, page)
		groups, starts := pageLinks(out)
		tree, list := strings.Index(out, `<nav class="tree"`), strings.Index(out, `<ul class="tree__list">`)
		if len(groups) != 1 || tree < 0 || starts[0] < tree || starts[0] > list {
			t.Errorf("%s: the drawer does not have one page-link group above its list", lang)
		}
	}
}

func TestPageTransferNoticeStartsBeforeContentAndEndsAtTheBodyTail(t *testing.T) {
	r := newRenderer(t)
	tail := regexp.MustCompile(`^<link data-page-transfer-complete rel="stylesheet" href="(/assets/page-complete\.css\?v=[0-9a-f]+)">\n</body>\n</html>$`)
	for _, lang := range []Lang{LangEN, LangKO} {
		for name, page := range allPages(lang) {
			out := render(t, r, page)
			start := strings.Index(out, "data-page-transfer-pending")
			content := strings.Index(out, `<main id="main"`)
			end := strings.Index(out, "<link data-page-transfer-complete")
			if start < 0 || start > content || end < content || !tail.MatchString(out[end:]) {
				t.Fatalf("%s (%s) notice is not tied to the body tail", name, lang)
			}
			if strings.Contains(out, "<style") || strings.Contains(out[:end], "page-complete.css") {
				t.Fatalf("%s (%s) needs inline CSS or hides its notice before completion", name, lang)
			}
		}
	}
	out := render(t, r, codePage(LangEN, &FileView{Path: "file.txt", Lines: []string{"line"}}))
	address := tail.FindStringSubmatch(out[strings.Index(out, "<link data-page-transfer-complete"):])[1]
	request := httptest.NewRequest(http.MethodGet, address, nil)
	request.URL.Path = strings.TrimPrefix(request.URL.Path, "/assets")
	response := httptest.NewRecorder()
	r.Assets().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != ".page-transfer-pending { display: none; }\n" || !strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("completion asset is not tiny, exact and cacheable: %d %q %q", response.Code, response.Body.String(), response.Header().Get("Cache-Control"))
	}
}
