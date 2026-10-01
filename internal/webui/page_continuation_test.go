package webui

import (
	"strings"
	"testing"
)

func TestPageTransferNoticeStartsBeforeContentAndEndsAtTheBodyTail(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range []Lang{LangEN, LangKO} {
		for name, page := range allPages(lang) {
			out := render(t, r, page)
			start := strings.Index(out, "data-page-transfer-pending")
			content := strings.Index(out, `<main id="main"`)
			end := strings.Index(out, "<style data-page-transfer-complete>")
			if start < 0 || start > content || end < content || !strings.HasPrefix(out[end:], "<style data-page-transfer-complete>.page-transfer-pending { display: none; }</style>\n</body>\n</html>") {
				t.Fatalf("%s (%s) notice is not tied to the body tail", name, lang)
			}
			if !strings.Contains(out[:end], "position: fixed") || !strings.Contains(out[:end], "visibility: hidden") || !strings.Contains(out[:end], "1.5s forwards") || strings.Contains(out[:end], ".page-transfer-pending { display: none;") {
				t.Fatalf("%s (%s) hides its notice before completion or changes document flow", name, lang)
			}
		}
	}
}
