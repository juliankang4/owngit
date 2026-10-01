package webui

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

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
