package webui

import (
	"context"
	"fmt"
	"html/template"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// sidebarOfOutput returns the rendered sidebar.
func sidebarOfOutput(t *testing.T, out string) string {
	t.Helper()
	at := strings.Index(out, `<nav class="sidebar"`)
	if at < 0 {
		t.Fatal("no sidebar")
	}
	menu := out[at:]
	return menu[:strings.Index(menu, "</nav>")]
}

func TestSidebarOutsideARepositoryListsPlacesThenRepositories(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, OverviewPage{Chrome: fullChrome(LangEN)})
	early := strings.Index(out, `<script src="/assets/sidebar.js?v=`)
	body := strings.Index(out, `<body>`)
	if early < 0 || early > body {
		t.Fatal("the sidebar must initialize before the body")
	}
	opening := out[early : early+strings.Index(out[early:], ">")]
	if strings.Contains(opening, "defer") || strings.Contains(opening, "async") {
		t.Fatal("the sidebar initializer must not wait for body parsing")
	}
	for _, lang := range Langs() {
		chrome := fullChrome(lang)
		chrome.Nav.NewImportURL = "/repositories/new-import"
		chrome.Nav.ActiveRepoID = ""
		chrome.Nav.Repositories[0].LastActivity = testNow
		for name, tc := range map[string]struct {
			page    Page
			current string
		}{
			"home":     {OverviewPage{Chrome: chrome, Activity: sampleGraph()}, `href="/" aria-current="page"`},
			"activity": {ActivityPage{Chrome: chrome}, `href="/activity" aria-current="page"`},
			"new":      {NewRepositoryPage{Chrome: chrome, SubmitURL: "/repositories"}, `class="iconbtn" href="/repositories/new" aria-current="page"`},
			"import":   {NewImportPage{Chrome: chrome, SubmitURL: "/repositories/new-import"}, `class="iconbtn" href="/repositories/new-import" aria-current="page"`},
		} {
			menu := sidebarOfOutput(t, render(t, r, tc.page))
			if !strings.Contains(menu, tc.current) || strings.Count(menu, `aria-current="page"`) != 1 {
				t.Errorf("%s %s: the current place is not marked exactly once:\n%s", lang, name, menu)
			}
			home := strings.Index(menu, wantText(lang, MsgNavHome))
			heading := strings.Index(menu, `<h2 class="sb__title" id="sb-repos-h">`)
			newRepo := strings.Index(menu, `href="/repositories/new"`)
			row := strings.Index(menu, `data-sb-name="forge-cli"`)
			if home < 0 || heading < home || newRepo < heading || row < newRepo {
				t.Errorf("%s %s: places, heading, actions and rows are out of order", lang, name)
			}
			if !strings.Contains(menu, `<span class="sb__total">2</span>`) || !strings.Contains(menu, `class="sb__when"`) {
				t.Errorf("%s %s: the repository count or recent time is missing", lang, name)
			}
			if strings.Contains(menu, "data-sb-filter") || !strings.Contains(menu, `data-sidebar-toggle>`) {
				t.Errorf("%s %s: filter or menu button state is wrong", lang, name)
			}
		}
	}
}

func TestSidebarFilterAppearsForManyRepositories(t *testing.T) {
	r := newRenderer(t)
	chrome := fullChrome(LangKO)
	chrome.Nav.Repositories = nil
	for index := 0; index < 9; index++ {
		chrome.Nav.Repositories = append(chrome.Nav.Repositories, NavRepository{ID: fmt.Sprint(index), Name: fmt.Sprintf("repo-%d", index), URL: "/x"})
	}
	menu := sidebarOfOutput(t, render(t, r, ActivityPage{Chrome: chrome}))
	if !strings.Contains(menu, `data-sb-filter hidden`) || !strings.Contains(menu, `data-ko-aria-label="저장소 찾기"`) {
		t.Errorf("nine repositories have no filter, hidden until the script runs:\n%s", menu)
	}
}

func TestSidebarFilterMatchesCanonicalNamesWithoutChangingRows(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required for the sidebar filter check")
	}
	script := section(t, scriptSource(t), "  var sideFilter =", "  /* File list drawer.")
	const check = `
const assert = require('node:assert/strict');
const vm = require('node:vm');
const names = ['café', 'cafe\u0301', '한글', '\u1112\u1161\u11ab\u1100\u1173\u11af', 'ﬁle', 'Tools', 'İstanbul', 'I\u0307stanbul'];
const rows = names.map((name, index) => ({
  name, href: '/repositories/repo-' + index, hidden: false,
  getAttribute(attribute) { assert.equal(attribute, 'data-sb-name'); return this.name; }
}));
const filter = {value: '', hidden: true, addEventListener(event, callback) { assert.equal(event, 'input'); this.input = callback; }};
const noMatch = {hidden: true};
vm.runInNewContext(process.argv[1], {
  document: {querySelector(selector) { return selector === '[data-sb-filter]' ? filter : noMatch; }},
  all(selector) { assert.equal(selector, '[data-sb-name]'); return rows; }
});
assert.equal(filter.hidden, false);
for (const [query, expected] of [
  ['café', [0,1]], ['cafe\u0301', [0,1]], ['한글', [2,3]],
  ['\u1112\u1161\u11ab\u1100\u1173\u11af', [2,3]], [' CAFÉ ', [0,1]],
  [' tools ', [5]], ['İstanbul', [6,7]], ['I\u0307stanbul', [6,7]],
  ['', [0,1,2,3,4,5,6,7]], ['cafe', []],
  ['file', []], ['ﬁle', [4]]
]) {
  filter.value = query;
  filter.input();
  assert.deepEqual(rows.flatMap((row, index) => row.hidden ? [] : [index]), expected, query);
  assert.equal(noMatch.hidden, expected.length !== 0, query);
  rows.forEach((row, index) => {
    assert.equal(row.name, names[index]);
    assert.equal(row.href, '/repositories/repo-' + index);
  });
}
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", check, script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sidebar filter: %v\n%s", err, output)
	}
}

func TestSidebarInsideARepositoryShowsItsSections(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		out := render(t, r, repoPage(fullChrome(lang), RepoTabCode))
		menu := sidebarOfOutput(t, out)
		for _, want := range []string{
			`class="sb__back" href="/"`, `<span class="sb__repo-n">forge-cli</span>`,
			`href="/repositories/r1/code" aria-current="page"`,
			// The narrow-window button names the repository and the section.
			"forge-cli, " + `<span data-en="Code" data-ko="코드">`,
		} {
			if !strings.Contains(menu, want) {
				t.Errorf("%s: repository sidebar lacks %q", lang, want)
			}
		}
		if strings.Contains(menu, `data-sb-name=`) {
			t.Errorf("%s: the repository sidebar still lists every repository", lang)
		}
		if strings.Contains(out, `class="rtabs`) || strings.Contains(out, `class="back" href="/"`) {
			t.Errorf("%s: the old tab strip or back button is still above the repository", lang)
		}
	}
}

func codePage(lang Lang, file *FileView) RepositoryPage {
	page := repoPage(fullChrome(lang), RepoTabCode)
	page.Code.File = file
	page.Code.Dir = "internal/pagination"
	return page
}

func TestCodeTabDocumentAndSource(t *testing.T) {
	r := newRenderer(t)
	doc := &FileView{Path: "README.md", Size: 20, Lines: []string{"# Title", "<b>raw</b>"}, Document: true,
		PreviewURL: "/c?path=README.md", SourceURL: "/c?path=README.md&view=source",
		Rendered: template.HTML(`<h1 id="md-title">Title</h1>`)}
	out := render(t, r, codePage(LangEN, doc))
	if !strings.Contains(out, `<article class="md"><h1 id="md-title">Title</h1></article>`) || strings.Contains(out, `class="codebox"`) {
		t.Error("the preview does not show the rendered document alone")
	}
	if !strings.Contains(out, `href="/c?path=README.md" aria-current="true"`) || strings.Contains(out, "data-wrap-toggle") {
		t.Error("the preview lacks its switch, or offers wrap for prose")
	}
	source := *doc
	source.ShowSource = true
	out = render(t, r, codePage(LangEN, &source))
	if strings.Contains(out, `<article class="md"`) || !strings.Contains(out, "&lt;b&gt;raw&lt;/b&gt;") || !strings.Contains(out, `href="/c?path=README.md&amp;view=source" aria-current="true"`) {
		t.Error("the source view does not show escaped lines")
	}
	if !strings.Contains(out, `data-wrap-toggle aria-pressed="false" hidden`) {
		t.Error("the source view has no wrap switch, off and hidden until the script runs")
	}
	// The backend renders nothing for the source view, and the switch back to
	// the preview stays.
	source.Rendered = ""
	out = render(t, r, codePage(LangEN, &source))
	if !strings.Contains(out, `<nav class="seg"`) || !strings.Contains(out, `href="/c?path=README.md&amp;view=source" aria-current="true"`) {
		t.Error("the source view of a document lost its switch to the preview")
	}
	for _, reason := range []MessageCode{MsgCodeNotShown, MsgCodeBusy, MsgCodeUnavailable} {
		refused := *doc
		refused.Rendered, refused.NotRendered = "", reason
		out = render(t, r, codePage(LangEN, &refused))
		if !strings.Contains(out, wantText(LangEN, reason)) || strings.Contains(out, `class="seg"`) || !strings.Contains(out, "&lt;b&gt;raw&lt;/b&gt;") {
			t.Errorf("a document not rendered (%s) does not show its source with the reason", reason)
		}
	}
	big := FileView{Path: "big.bin", Size: 11 << 20, Binary: true, RawURL: "/raw", RawTooLarge: true}
	out = render(t, r, codePage(LangKO, &big))
	if strings.Contains(out, `href="/raw"`) || !strings.Contains(out, wantText(LangKO, MsgCodeRawTooLarge)) {
		t.Error("a file above the download limit still offers the download")
	}
	big.Truncated = true
	if out = render(t, r, codePage(LangKO, &big)); strings.Contains(out, "code.truncated") || strings.Contains(out, `class="hm__warn"`) {
		t.Error("a binary file, which shows no lines, says only its beginning is shown")
	}
	// A file refused for the memory its stored delta needs says so, and does
	// not call the file itself too large: the file can be small.
	refused := FileView{Path: "small.txt", Size: 7 << 20, TooLarge: true, TooLargeMemory: true}
	out = render(t, r, codePage(LangKO, &refused))
	if !strings.Contains(out, wantText(LangKO, MsgCodeTooLargeMemory)) || strings.Contains(out, wantText(LangKO, MsgCodeTooLarge)) {
		t.Error("a file refused for the memory of its stored delta does not name that reason")
	}
}

func TestCodeTabFileHasDrawerAndFullWidth(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabCode)
	page.Code.Dir = "internal/pagination"
	out := render(t, r, page)
	for _, want := range []string{
		`<div class="app app--wide">`, `<details class="drawer" data-drawer>`, `<p class="tree__h mono">/internal/pagination</p>`,
		`<div class="rhead visually-hidden">`, `class="refbar__label visually-hidden"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("code file page lacks %q", want)
		}
	}
	if strings.Contains(out, `class="code__split"`) {
		t.Error("the file still sits beside a fixed tree column")
	}
	overview := render(t, r, repoPage(fullChrome(LangEN), RepoTabOverview))
	if strings.Contains(overview, "app--wide") {
		t.Error("the overview widened; only code and diffs may")
	}
}

func TestCodeTabFolderShowsListingAndReadme(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabCode)
	page.Code.File = nil
	page.Code.Readme = &ReadmeView{Path: "internal/README.md", URL: "/r", Rendered: template.HTML("<p>Hello</p>")}
	out := render(t, r, page)
	if !strings.Contains(out, `<nav class="flist"`) || !strings.Contains(out, `<article class="md"><p>Hello</p></article>`) {
		t.Error("the folder lacks its listing or README")
	}
	if strings.Contains(out, "data-drawer") {
		t.Error("a folder has a drawer; its listing is the page")
	}
	page.Code.Readme = &ReadmeView{Path: "internal/README.md", URL: "/r", Note: MsgReadmeNotShown}
	out = render(t, r, page)
	if !strings.Contains(out, `<a href="/r" dir="auto">internal/README.md</a>`) || !strings.Contains(out, wantText(LangEN, MsgReadmeNotShown)) || strings.Contains(out, `<article class="md"`) {
		t.Error("a README that was not rendered does not say so with a link to it")
	}
}

func manyDiffFiles(n int) []DiffFile {
	files := make([]DiffFile, 0, n)
	for index := 0; index < n; index++ {
		files = append(files, DiffFile{Path: fmt.Sprintf("pkg/f%02d.go", index), Status: "modified", Additions: 1, Deletions: 1,
			Hunks: []DiffHunk{{Header: "@@ -1 +1 @@", Lines: []DiffLine{{Kind: "del", OldLine: 1, Text: "old"}, {Kind: "add", NewLine: 1, Text: "new"}}}}})
	}
	return files
}

func TestUnavailableTextComparisonKeepsItsOwnNotice(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		page := repoPage(fullChrome(lang), RepoTabCommits)
		page.Commits.Detail.Files = []DiffFile{
			{Path: "logs/service.txt", Status: "modified", Binary: true, TextDiffUnavailable: true},
			{Path: "docs/logo.png", Status: "modified", Binary: true},
		}
		out := render(t, r, page)
		if !strings.Contains(out, wantText(lang, MsgDiffTextUnavailableNote)) {
			t.Errorf("%s: an unavailable text comparison has no explanation", lang)
		}
		if !strings.Contains(out, wantText(lang, MsgDiffTextUnavailable)) {
			t.Errorf("%s: the file list does not mark the text comparison as unavailable", lang)
		}
		if !strings.Contains(out, wantText(lang, MsgCommitBinaryFile)) {
			t.Errorf("%s: a binary file lost its own note", lang)
		}
		if strings.Contains(out, `<span class="add">+0</span>`) {
			t.Errorf("%s: a file with no line counts shows them as zero", lang)
		}
	}
}

func TestDiffListCollapsesPastTwentyFiles(t *testing.T) {
	r := newRenderer(t)
	for _, lang := range Langs() {
		page := repoPage(fullChrome(lang), RepoTabCommits)
		page.Commits.Detail.Files = manyDiffFiles(46)
		page.Commits.Detail.Files[3].Binary, page.Commits.Detail.Files[3].Hunks = true, nil
		page.Commits.Detail.Files[4].Hunks, page.Commits.Detail.Files[4].NotLoaded, page.Commits.Detail.Files[4].URL = nil, true, "/one?path=pkg/f04.go"
		out := render(t, r, page)
		list := out[strings.Index(out, `<nav class="dlist"`):]
		list = list[:strings.Index(list, "</nav>")]
		shown := list[:strings.Index(list, `<details class="dlist__more">`)]
		if strings.Count(shown, `class="dlist__row"`) != 20 || strings.Count(list, `class="dlist__row"`) != 46 {
			t.Errorf("%s: the list does not show 20 rows and keep the rest behind the disclosure", lang)
		}
		if !strings.Contains(list, fmt.Sprintf(Text(lang, MsgDiffMoreFiles), 26)) {
			t.Errorf("%s: the disclosure does not say how many files it holds", lang)
		}
		if strings.Count(out, `<section class="dfile"`) != 46 || !strings.Contains(out, `id="f-45"`) || !strings.Contains(out, `href="#f-45"`) {
			t.Errorf("%s: not every file is on the page with an anchor", lang)
		}
		if !strings.Contains(out, `data-diff-fold hidden`) || !strings.Contains(out, `data-diff-all hidden`) {
			t.Errorf("%s: the fold controls do not wait for the script", lang)
		}
		if !strings.Contains(out, `<a href="/one?path=pkg/f04.go">`) || !strings.Contains(out, wantText(lang, MsgDiffNotLoaded)) {
			t.Errorf("%s: a file left out of the page has no way to its own diff", lang)
		}
		if strings.Contains(out, `class="commits__split"`) || !strings.Contains(out, `class="back" href="/repositories/r1/commits"`) {
			t.Errorf("%s: the commit is not full width with a way back to the list", lang)
		}
	}
}

func TestDiffStatusIsAWord(t *testing.T) {
	r := newRenderer(t)
	page := repoPage(fullChrome(LangEN), RepoTabCommits)
	page.Commits.Detail.Files = []DiffFile{
		{Path: "a.go", Status: "added", Additions: 1},
		{Path: "b.go", Status: "deleted", Deletions: 1},
	}
	out := render(t, r, page)
	for _, want := range []string{`<span class="dstat dstat--added">`, `<span class="dstat dstat--deleted">`, wantText(LangEN, MsgDiffNoLines)} {
		if !strings.Contains(out, want) {
			t.Errorf("diff lacks %q", want)
		}
	}
}
