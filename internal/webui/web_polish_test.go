package webui

import (
	"strings"
	"testing"
)

// The sidebar's New repository and Import entries fit on one line: English
// shows "New" and "Import", while assistive technology and hovering get the
// full names. Korean already fits and keeps its words. The dashboard's own
// section links keep their full names.
func TestSidebarEntriesUseShortVisibleLabels(t *testing.T) {
	r := newRenderer(t)
	for _, test := range []struct {
		lang Lang
		want []string
	}{
		{LangEN, []string{
			`title="New repository"`, `<span aria-hidden="true"><span data-en="New" data-ko="새 저장소">New</span></span>`,
			`<span class="visually-hidden"><span data-en="New repository" data-ko="새 저장소">New repository</span></span>`,
			`title="Import a repository"`, `<span aria-hidden="true"><span data-en="Import" data-ko="가져오기">Import</span></span>`,
			`<span class="visually-hidden"><span data-en="Import a repository" data-ko="저장소 가져오기">Import a repository</span></span><span class="adminlock"`,
			`<span class="sec__hint"><a href="/repositories/new"><span data-en="New repository" data-ko="새 저장소">New repository</span></a></span>`,
		}},
		{LangKO, []string{
			`title="새 저장소"`, `<span aria-hidden="true"><span data-en="New" data-ko="새 저장소">새 저장소</span></span>`,
			`<span aria-hidden="true"><span data-en="Import" data-ko="가져오기">가져오기</span></span>`,
			`<span class="visually-hidden"><span data-en="Import a repository" data-ko="저장소 가져오기">저장소 가져오기</span></span>`,
		}},
	} {
		chrome := fullChrome(test.lang)
		chrome.Nav.NewImportURL = "/repositories/new-import"
		out := render(t, r, OverviewPage{Chrome: chrome, Activity: sampleGraph()})
		for _, want := range test.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s sidebar lacks %s", test.lang, want)
			}
		}
	}
}
