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
