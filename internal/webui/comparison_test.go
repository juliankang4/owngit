package webui

import (
	"strings"
	"testing"
)

// An incomplete comparison says so, in both languages, before the changes,
// and a comparison that is not possible says why instead of claiming the
// branches have no changes.
func TestPullRequestComparisonStatesAreLabeled(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range []Lang{LangEN, LangKO} {
		page := pullRequestPage(fullChrome(lang), prFixtureFailing)
		page.ChangesBase = "0123456789abcdef0123456789abcdef01234567"
		page.DiffTruncated = true
		html := render(t, r, page)
		label := strings.Index(html, wantText(lang, MsgPRChangesCut))
		list := strings.Index(html, `class="dlist"`)
		if label < 0 || list < 0 || label > list || !strings.Contains(html, "0123456789") || !strings.Contains(html, wantText(lang, MsgPRChangesBase)) {
			t.Fatalf("%s: truncated label at %d, file list at %d", lang, label, list)
		}

		page.DiffTruncated, page.FilesTruncated = true, true
		html = render(t, r, page)
		if !strings.Contains(html, wantText(lang, MsgPRChangesFilesCut)) || strings.Contains(html, wantText(lang, MsgPRChangesCut)) {
			t.Fatalf("%s: a cut file list is not labeled as such", lang)
		}
		page.Changes = nil
		if html = render(t, r, page); strings.Contains(html, wantText(lang, MsgPRChangesNone)) {
			t.Fatalf("%s: a cut list with no files read claims there are no changes", lang)
		}

		for _, reason := range []MessageCode{MsgPRChangesNoBase, MsgPRChangesManyBases} {
			page := pullRequestPage(fullChrome(lang), prFixtureFailing)
			page.Changes, page.ChangesUnavailable, page.ChangesReason = nil, true, reason
			html := render(t, r, page)
			if !strings.Contains(html, wantText(lang, reason)) || strings.Contains(html, wantText(lang, MsgPRChangesNone)) || strings.Contains(html, `class="dfile"`) {
				t.Fatalf("%s: %s is not shown as the reason", lang, reason)
			}
		}
	}
}
