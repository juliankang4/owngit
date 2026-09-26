package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"owngit/internal/markdown"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/testfixture"
)

// seedRepository creates a repository named id whose main branch holds files,
// committed at when, and returns the working copy and the commit.
func seedRepository(t *testing.T, app *App, id string, files map[string]string, when time.Time) (string, string) {
	t.Helper()
	if _, err := app.Repositories.Create(context.Background(), id, ""); err != nil {
		t.Fatal(err)
	}
	remote, _ := app.Repositories.Path(id)
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Code Author")
	apiRunGit(t, work, "config", "user.email", "code@example.invalid")
	apiRunGit(t, work, "remote", "add", "origin", remote)
	return work, commitFiles(t, work, files, "seed", when)
}

func commitFiles(t *testing.T, work string, files map[string]string, message string, when time.Time) string {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(work, filepath.FromSlash(name))
		noErr(t, os.MkdirAll(filepath.Dir(full), 0o700))
		noErr(t, os.WriteFile(full, []byte(content), 0o600))
	}
	apiRunGit(t, work, "add", "-A")
	commit := exec.Command("git", "commit", "-q", "-m", message)
	commit.Dir = work
	stamp := when.Format(time.RFC3339)
	commit.Env = testfixture.GitEnvironment(append(os.Environ(), "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp))
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, output)
	}
	apiRunGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	return apiGitOutput(t, work, "rev-parse", "HEAD")
}

const hostileReadme = "# Guide\n\n" +
	"<script>alert('readme')</script>\n\n" +
	"<img src=x onerror=alert(1)>\n\n" +
	"[run](javascript:alert(1)) [data](data:text/html;base64,PHNjcmlwdD4=) [setup](docs/setup.md) [out](../../etc/passwd)\n\n" +
	"![diagram](docs/pic.svg) ![remote](https://images.example.invalid/x.png)\n"

const hostileSVG = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert('svg')</script><rect width="4" height="4"/></svg>`

func TestCodeViewRendersDocumentsWithoutRepositoryMarkup(t *testing.T) {
	app := newConfiguredApp(t)
	seedRepository(t, app, "docs-project", map[string]string{
		"README.md":     hostileReadme,
		"docs/setup.md": "Setup\n",
		"docs/pic.svg":  hostileSVG,
		"main.go":       "package main\n",
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	base := server.URL + "/repositories/docs-project/code?ref=refs%2Fheads%2Fmain"

	preview, status := dashboardGET(t, client, base+"&path=README.md")
	if status != http.StatusOK {
		t.Fatalf("preview status=%d", status)
	}
	document := preview[strings.Index(preview, `<article class="md">`):]
	document = document[:strings.Index(document, "</article>")]
	for _, want := range []string{
		`<h1 id="md-guide">Guide</h1>`,
		`href="/repositories/docs-project/code?ref=refs%2Fheads%2Fmain&amp;path=docs%2Fsetup.md"`,
		`src="/repositories/docs-project/raw?ref=refs%2Fheads%2Fmain&amp;path=docs%2Fpic.svg"`,
		`<a href="https://images.example.invalid/x.png" rel="noopener noreferrer">remote</a>`,
	} {
		if !strings.Contains(document, want) {
			t.Errorf("rendered document lacks %s:\n%s", want, document)
		}
	}
	for _, bad := range []string{"<script", "onerror", "javascript:", "data:text", "passwd\"", "<img src=\"https://", "<img src=x"} {
		if strings.Contains(document, bad) {
			t.Errorf("rendered document contains %q:\n%s", bad, document)
		}
	}
	if !strings.Contains(preview, `&amp;view=source">`) || !strings.Contains(preview, `ref=refs%2Fheads%2Fmain" aria-current="true">`) {
		t.Error("the preview does not offer its source view")
	}

	source, _ := dashboardGET(t, client, base+"&path=README.md&view=source")
	if strings.Contains(source, `<article class="md">`) || !strings.Contains(source, "&lt;script&gt;alert(&#39;readme&#39;)&lt;/script&gt;") {
		t.Error("the source view does not show the escaped file text")
	}
	if !strings.Contains(source, `data-wrap-toggle aria-pressed="false" hidden`) {
		t.Error("the source view lacks the wrap switch, off and hidden until the script runs")
	}

	folder, _ := dashboardGET(t, client, base)
	if !strings.Contains(folder, `<nav class="flist"`) || !strings.Contains(folder, `<section class="readme"`) || strings.Contains(folder, "<script>alert") {
		t.Error("the folder does not list its files with its rendered README")
	}
	if strings.Contains(folder, `data-drawer`) {
		t.Error("a folder page has a file drawer; its listing is the page")
	}
	code, _ := dashboardGET(t, client, base+"&path=main.go")
	if !strings.Contains(code, `<details class="drawer" data-drawer>`) || !strings.Contains(code, `class="codetable__t">package main</td>`) {
		t.Error("a code file lacks its drawer or its lines")
	}
}

func TestRawFilesNeverRunInThePage(t *testing.T) {
	app := newConfiguredApp(t)
	seedRepository(t, app, "raw-project", map[string]string{
		"docs/pic.svg": hostileSVG,
		"page.html":    "<script>alert('html')</script>",
		"사진.png":       "\x89PNG\r\n\x1a\nfake",
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}
	raw := server.URL + "/repositories/raw-project/raw?ref=refs%2Fheads%2Fmain&path="

	for path, wantType := range map[string]string{
		"docs/pic.svg": "image/svg+xml",
		"page.html":    "application/octet-stream",
		"사진.png":       "image/png",
	} {
		result := browserGET(t, client, raw+strings.ReplaceAll(path, "/", "%2F"))
		if result.status != http.StatusOK {
			t.Fatalf("%s status=%d", path, result.status)
		}
		header := result.header
		if got := header.Get("Content-Type"); got != wantType {
			t.Errorf("%s Content-Type=%q, want %q", path, got, wantType)
		}
		if got := header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
			t.Errorf("%s Content-Disposition=%q, want an attachment", path, got)
		}
		if header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s is not marked nosniff", path)
		}
		csp := header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "sandbox") || strings.Contains(csp, "script-src") {
			t.Errorf("%s policy %q allows the file to run", path, csp)
		}
		if !strings.HasPrefix(header.Get("Cache-Control"), "private") {
			t.Errorf("%s may be kept by a shared cache", path)
		}
	}
	if got := browserGET(t, client, raw+"%EC%82%AC%EC%A7%84.png").header.Get("Content-Disposition"); !strings.Contains(got, "filename*=utf-8''%EC%82%AC%EC%A7%84.png") {
		t.Errorf("a Korean file name is not carried: %q", got)
	}
	for _, missing := range []string{"nothing.txt", "docs", "..%2Fetc%2Fpasswd"} {
		if status := browserGET(t, client, raw+missing).status; status != http.StatusNotFound {
			t.Errorf("raw %s status=%d, want 404", missing, status)
		}
	}
}

func TestRawFilesNeedTheSameAccessAsTheCodeView(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	noErr(t, os.MkdirAll(repositoryRoot, 0o700))
	canonical, err := filepath.EvalSymlinks(repositoryRoot)
	noErr(t, err)
	accessHash, _ := auth.HashPassword("shared-password")
	adminHash, _ := auth.HashPassword("admin-password")
	noErr(t, store.CompleteSetup(context.Background(), canonical, "password", accessHash, adminHash, true))
	app.Repositories.SetRoot(canonical)
	seedRepository(t, app, "private-raw", map[string]string{"secret.txt": "private\n"}, time.Now())
	server := serve(t, app.Handler())
	client, _ := newBrowserClient(t)
	result := browserGET(t, client, server.URL+"/repositories/private-raw/raw?path=secret.txt")
	if result.status != http.StatusSeeOther || strings.Contains(result.body, "private\n") {
		t.Fatalf("raw file answered without access: status=%d", result.status)
	}
	head, err := http.NewRequest(http.MethodHead, server.URL+"/repositories/private-raw/raw?path=secret.txt", nil)
	noErr(t, err)
	if result := browserRequest(t, client, head); result.status != http.StatusSeeOther || result.header.Get("Content-Disposition") != "" {
		t.Fatalf("HEAD on a raw file answered without access: status=%d", result.status)
	}
}

func TestCommitShowsEveryChangedFile(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, _ := seedRepository(t, app, "big-commit", map[string]string{"keep.txt": "keep\n"}, when)
	files := map[string]string{
		"with space/a b.txt": "spaced line\n",
		"한글 파일.md":           "한글 줄\n",
		"image.bin":          "\x00\x01binary",
	}
	for index := 0; index < 42; index++ {
		files[fmt.Sprintf("pkg/file%02d.go", index)] = fmt.Sprintf("package pkg\n\nconst Value%02d = %d\n", index, index)
	}
	oid := commitFiles(t, work, files, "add many files", when.Add(time.Hour))
	server := serve(t, app.Handler())
	client := &http.Client{}

	body, status := dashboardGET(t, client, server.URL+"/repositories/big-commit/commits/"+oid+"?ref=refs%2Fheads%2Fmain")
	if status != http.StatusOK {
		t.Fatalf("commit status=%d", status)
	}
	if got := strings.Count(body, `<section class="dfile"`); got != 45 {
		t.Errorf("rendered %d files, want 45", got)
	}
	for _, want := range []string{
		"Show 25 more files", `<details class="dlist__more">`, `href="#f-44"`, `id="f-44"`,
		"const Value41 = 41", "spaced line", "한글 줄", "Binary file", `class="back" href="/repositories/big-commit/commits?ref=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("commit page lacks %q", want)
		}
	}
	if strings.Contains(body, `class="fchips"`) || strings.Contains(body, `class="commits__split"`) {
		t.Error("the commit still uses the chip wall or the split layout")
	}

	one, _ := dashboardGET(t, client, server.URL+"/repositories/big-commit/commits/"+oid+"?ref=refs%2Fheads%2Fmain&path=pkg%2Ffile07.go")
	if strings.Count(one, `<section class="dfile"`) != 1 || !strings.Contains(one, "const Value07 = 7") || !strings.Contains(one, "Show every changed file") {
		t.Error("a commit opened for one file does not show that file alone with a way back")
	}
}

// One large file must not use up the page and hide the small files after it.
func TestLargeFilesInACommitDoNotHideTheRest(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, _ := seedRepository(t, app, "huge-commit", map[string]string{"keep.txt": "keep\n"}, when)
	files := map[string]string{
		"a-big.txt":       strings.Repeat(strings.Repeat("x", 99)+"\n", 30000), // about 3 MB
		"b-bundle.min.js": strings.Repeat("var a=1;", 64<<10),                  // one 512 KiB line
	}
	for index := 0; index < 12; index++ {
		files[fmt.Sprintf("c-small%02d.txt", index)] = fmt.Sprintf("small file %d\n", index)
	}
	oid := commitFiles(t, work, files, "add big and small files", when.Add(time.Hour))
	server := serve(t, app.Handler())
	body, status := dashboardGET(t, &http.Client{}, server.URL+"/repositories/huge-commit/commits/"+oid+"?ref=refs%2Fheads%2Fmain")
	if status != http.StatusOK {
		t.Fatalf("commit status=%d", status)
	}
	if got := strings.Count(body, `<section class="dfile"`); got != 14 {
		t.Errorf("rendered %d files, want 14", got)
	}
	for index := 0; index < 12; index++ {
		if !strings.Contains(body, fmt.Sprintf("small file %d", index)) {
			t.Errorf("small file %d was hidden by the large files before it", index)
		}
	}
	// Each note carries both languages, so it holds the English words twice.
	if strings.Count(body, "were not loaded") != 2*2 || !strings.Contains(body, "path=a-big.txt") || !strings.Contains(body, "path=b-bundle.min.js") {
		t.Error("the large files are not marked with links to their own diffs")
	}
	if len(body) > 1<<20 {
		t.Errorf("the page is %d bytes; the large files were loaded anyway", len(body))
	}
}

// Files that together pass the page limit are listed, and those past it are
// marked with links.
func TestCommitPastThePageLimitMarksTheRest(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, _ := seedRepository(t, app, "wide-commit", map[string]string{"keep.txt": "keep\n"}, when)
	files := map[string]string{}
	for index := 0; index < 8; index++ {
		files[fmt.Sprintf("part%d.txt", index)] = strings.Repeat(strings.Repeat("y", 99)+"\n", 2000) // 200 KB each
	}
	oid := commitFiles(t, work, files, "add medium files", when.Add(time.Hour))
	server := serve(t, app.Handler())
	body, _ := dashboardGET(t, &http.Client{}, server.URL+"/repositories/wide-commit/commits/"+oid+"?ref=refs%2Fheads%2Fmain")
	if strings.Count(body, `<section class="dfile"`) != 8 || !strings.Contains(body, "path=part7.txt") || !strings.Contains(body, "were not loaded") {
		t.Error("files past the page limit are not listed with links to their own diffs")
	}
	if !strings.Contains(body, "part0.txt") || !strings.Contains(body, `class="difftable`) {
		t.Error("the first files lost their diffs")
	}
}

// Short lines make a small diff into many table rows, so the page also
// stops at a number of lines.
func TestCommitOfShortLinesStopsAtTheLineLimit(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, _ := seedRepository(t, app, "short-lines", map[string]string{"keep.txt": "keep\n"}, when)
	oid := commitFiles(t, work, map[string]string{
		"a.txt": strings.Repeat("a\n", maximumCommitDiffLines*6/10),
		"b.txt": strings.Repeat("b\n", maximumCommitDiffLines*6/10),
		"c.txt": "c\n",
	}, "add short lines", when.Add(time.Hour))
	server := serve(t, app.Handler())
	body, _ := dashboardGET(t, &http.Client{}, server.URL+"/repositories/short-lines/commits/"+oid+"?ref=refs%2Fheads%2Fmain")
	if rows := strings.Count(body, "<tr"); rows > maximumCommitDiffLines+100 {
		t.Errorf("the page has %d rows, past the line limit", rows)
	}
	// a.txt and the one-line c.txt fit; b.txt would pass the limit.
	if !strings.Contains(body, "path=b.txt") || strings.Count(body, "were not loaded") != 2 || strings.Count(body, `<table class="difftable">`) != 2 {
		t.Error("only the file past the line limit should be marked with a link")
	}
}

func TestPullRequestFilesPastTheLimitAreMarked(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, base := seedRepository(t, app, "many-files", map[string]string{"keep.txt": "keep\n"}, when)
	files := map[string]string{"z-image.png": "\x89PNG\r\n\x1a\n\x00binary"}
	// Each file fits alone; together they pass the page's line limit.
	for index := 0; index < 3; index++ {
		files[fmt.Sprintf("f%03d.txt", index)] = strings.Repeat(fmt.Sprintf("line %d\n", index), maximumCommitDiffLines*6/10)
	}
	head := commitFiles(t, work, files, "add many files", when.Add(time.Hour))
	changes, err := app.comparePullRequestRevisions(context.Background(), "many-files", head, base)
	if err != nil {
		t.Fatal(err)
	}
	diffs := changes.Files
	if !changes.PatchesIncomplete || changes.FilesIncomplete || len(diffs) != 4 {
		t.Fatalf("changes=%+v files=%d", changes, len(diffs))
	}
	for index, file := range diffs {
		switch {
		case file.Path == "z-image.png":
			if !file.Binary || file.NotLoaded {
				t.Errorf("%s: binary=%v not loaded=%v", file.Path, file.Binary, file.NotLoaded)
			}
		case index == 0:
			if file.NotLoaded || len(file.Hunks) == 0 {
				t.Errorf("%s inside the limit was not loaded", file.Path)
			}
		default:
			if !file.NotLoaded || len(file.Hunks) != 0 || file.Additions != maximumCommitDiffLines*6/10 {
				t.Errorf("%s past the limit: not loaded=%v hunks=%d additions=%d", file.Path, file.NotLoaded, len(file.Hunks), file.Additions)
			}
		}
	}
}

// A README that goldmark would render for minutes is refused at once; the
// folder still lists its files, and the file view shows the source with the
// reason. The README is the review's round 2 shape: empty list items, which
// do not end a paragraph, between lines of emphasis delimiters.
func TestHostileReadmeFallsBackToSource(t *testing.T) {
	app := newConfiguredApp(t)
	hostile := "x\n" + strings.Repeat("* \n"+strings.Repeat("*a_", 497)+"\n", 170)
	seedRepository(t, app, "slow-docs", map[string]string{
		"README.md": hostile,
		"main.go":   "package main\n",
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}
	base := server.URL + "/repositories/slow-docs/code?ref=refs%2Fheads%2Fmain"
	start := time.Now()
	folder, status := dashboardGET(t, client, base)
	if elapsed := time.Since(start); status != http.StatusOK || elapsed > 5*time.Second {
		t.Fatalf("folder status=%d after %v", status, elapsed)
	}
	if !strings.Contains(folder, "main.go") || !strings.Contains(folder, "too large or too complex to show here") || strings.Contains(folder, `<article class="md">`) {
		t.Error("the folder does not list its files and name its README without rendering it")
	}
	file, _ := dashboardGET(t, client, base+"&path=README.md")
	if !strings.Contains(file, "too large or too complex to show formatted") || !strings.Contains(file, `class="codetable__t"`) || strings.Contains(file, `<nav class="seg"`) {
		t.Error("the document does not fall back to its source with the reason")
	}
}

// Documents that would take gigabytes to render (the review's round 3
// cases) cost the server no memory: the padded table is refused by the
// estimate, and the reused reference link, which passes it, is stopped in
// the child process. A second view starts nothing.
func TestAmplifyingReadmesDoNotGrowTheServer(t *testing.T) {
	app := newConfiguredApp(t)
	amplifier := "[a]: http://x.y/" + strings.Repeat("a", 40000) + " \"" + strings.Repeat("t", 40000) + "\"\n\n" + strings.Repeat("[a]\n", 10000)
	table := strings.Repeat("|a", 4000) + "|\n" + strings.Repeat("|-", 4000) + "|\n" + strings.Repeat("a\n", 2000)
	seedRepository(t, app, "amplifiers", map[string]string{
		"links/README.md": amplifier,
		"table/README.md": table,
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for _, folder := range []string{"links", "table", "links", "table"} {
		body, status := dashboardGET(t, client, server.URL+"/repositories/amplifiers/code?ref=refs%2Fheads%2Fmain&path="+folder)
		if status != http.StatusOK || !strings.Contains(body, "too large or too complex to show here") || strings.Contains(body, `<article class="md">`) {
			t.Fatalf("%s: status %d without the note", folder, status)
		}
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if grown := int64(after.HeapSys) - int64(before.HeapSys); grown > 64<<20 {
		t.Errorf("the server heap grew by %d MiB", grown>>20)
	}
}

// When the server cannot run its render helper, pages say so rather than
// blaming the document, and the document renders once the helper is back.
func TestUnavailableHelperIsNamedOnThePage(t *testing.T) {
	app := newConfiguredApp(t)
	seedRepository(t, app, "no-helper", map[string]string{
		"README.md": "# Plain readme\n\nNothing unusual.\n",
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}
	base := server.URL + "/repositories/no-helper/code?ref=refs%2Fheads%2Fmain"
	executable, err := os.Executable()
	noErr(t, err)
	markdown.SetHelper(filepath.Join(t.TempDir(), "missing-owngit"))
	defer markdown.SetHelper(executable)

	folder, _ := dashboardGET(t, client, base)
	if !strings.Contains(folder, "Formatted view is unavailable on this server. Open the README") || strings.Contains(folder, "too large or too complex") {
		t.Error("the folder does not say the formatted view is unavailable")
	}
	file, _ := dashboardGET(t, client, base+"&path=README.md")
	if !strings.Contains(file, "Formatted view is unavailable on this server, so the source is shown.") || !strings.Contains(file, "이 서버에서는 서식 보기를 쓸 수 없어") {
		t.Error("the file view does not say the formatted view is unavailable")
	}

	markdown.SetHelper(executable)
	if folder, _ = dashboardGET(t, client, base); !strings.Contains(folder, `<article class="md">`) {
		t.Error("the README was remembered as a failure and stays unrendered")
	}
}

func TestRawFilesAnswerHeadAndRefuseLargeFilesInWords(t *testing.T) {
	app := newConfiguredApp(t)
	seedRepository(t, app, "raw-sizes", map[string]string{
		"small.txt": "small\n",
		"big.bin":   strings.Repeat("\x00\x01", maximumRawBytes/2+1),
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}
	raw := server.URL + "/repositories/raw-sizes/raw?ref=refs%2Fheads%2Fmain&path="

	head, err := http.NewRequest(http.MethodHead, raw+"small.txt", nil)
	noErr(t, err)
	result := browserRequest(t, client, head)
	if result.status != http.StatusOK || result.body != "" || result.header.Get("Content-Length") != "6" ||
		!strings.HasPrefix(result.header.Get("Content-Disposition"), "attachment") || !strings.Contains(result.header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("HEAD status=%d body=%q headers=%v", result.status, result.body, result.header)
	}

	big := browserGET(t, client, raw+"big.bin")
	if big.status != http.StatusForbidden || !strings.Contains(big.body, "Files over 10 MB cannot be downloaded") || !strings.Contains(big.body, "10MB가 넘는 파일은") {
		t.Errorf("a file over the limit got status %d without the explanation", big.status)
	}
	missing := browserGET(t, client, raw+"nothing.txt")
	if missing.status != http.StatusNotFound || !strings.Contains(missing.header.Get("Content-Type"), "text/html") {
		t.Errorf("a missing file got status %d, %s", missing.status, missing.header.Get("Content-Type"))
	}
	view, _ := dashboardGET(t, client, server.URL+"/repositories/raw-sizes/code?ref=refs%2Fheads%2Fmain&path=big.bin")
	if strings.Contains(view, "/raw?") || !strings.Contains(view, "Files over 10 MB cannot be downloaded") {
		t.Error("the file view offers a download it cannot serve")
	}
}

func TestSidebarListsRecentRepositoriesFirst(t *testing.T) {
	app := newConfiguredApp(t)
	seedRepository(t, app, "aaa-old", map[string]string{"a.txt": "a\n"}, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	seedRepository(t, app, "zzz-new", map[string]string{"z.txt": "z\n"}, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}
	// The dashboard reads every repository's snapshot; the sidebar then
	// orders by what those snapshots found, without reading Git itself.
	dashboardGET(t, client, server.URL+"/")
	body, _ := dashboardGET(t, client, server.URL+"/activity")
	menu := body[strings.Index(body, `<nav class="sidebar"`):]
	if strings.Index(menu, `data-sb-name="zzz-new"`) > strings.Index(menu, `data-sb-name="aaa-old"`) {
		t.Error("the more recently active repository is not listed first")
	}
	if !strings.Contains(menu, `href="/activity" aria-current="page"`) {
		t.Error("All activity is not marked as the current place")
	}
}

func TestSplitPatchByFile(t *testing.T) {
	patch := "diff --git a/plain.txt b/plain.txt\n" +
		"index 1..2 100644\n--- a/plain.txt\n+++ b/plain.txt\n@@ -1 +1 @@\n-old\n+new\n" +
		"diff --git a/a b/c b/a b/c\nnew file mode 100644\n--- /dev/null\n+++ b/a b/c\n@@ -0,0 +1 @@\n+spaced\n" +
		"diff --git \"a/\\355\\225\\234.txt\" \"b/\\355\\225\\234.txt\"\n--- /dev/null\n+++ \"b/\\355\\225\\234.txt\"\n@@ -0,0 +1 @@\n+korean\n" +
		"diff --git a/link b/link\ndeleted file mode 120000\n--- a/link\n+++ /dev/null\n@@ -1 +0,0 @@\n-target\n" +
		"diff --git a/link b/link\nnew file mode 100644\n--- /dev/null\n+++ b/link\n@@ -0,0 +1 @@\n+file now\n" +
		"diff --git a/last.txt b/last.txt\n--- a/last.txt\n+++ b/last.txt\n@@ -1 +1 @@\n-cut"
	sections := splitPatchByFile(patch, true)
	if !strings.Contains(sections["plain.txt"], "+new") || !strings.Contains(sections["a b/c"], "+spaced") || !strings.Contains(sections["한.txt"], "+korean") {
		t.Fatalf("sections were not keyed by path: %q", sections)
	}
	if !strings.Contains(sections["link"], "-target") || !strings.Contains(sections["link"], "+file now") {
		t.Error("a type change lost one of its two parts")
	}
	if _, ok := sections["last.txt"]; ok {
		t.Error("the cut-off last part was kept")
	}
	if got := len(splitPatchByFile(patch, false)); got != 5 {
		t.Errorf("a complete patch gave %d parts, want 5", got)
	}
}

// A raster picture is shown in the file view through the raw endpoint, with
// its pixel size. SVG, which can carry script, stays a download (its text is
// shown as source), and a file whose bytes do not match its image type is an
// ordinary binary file.
func TestCodeViewShowsPicturesButNeverSVG(t *testing.T) {
	var picture bytes.Buffer
	noErr(t, png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 3, 2))))
	app := newConfiguredApp(t)
	seedRepository(t, app, "pictures", map[string]string{
		"logo.png":     picture.String(),
		"docs/pic.svg": hostileSVG,
		"fake.png":     "\x00\x01not a picture",
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}
	code := server.URL + "/repositories/pictures/code?ref=refs%2Fheads%2Fmain&path="

	body, status := dashboardGET(t, client, code+"logo.png")
	wantImage := `<img class="imgview__img" src="/repositories/pictures/raw?path=logo.png&amp;ref=refs%2Fheads%2Fmain" alt="logo.png"`
	if status != http.StatusOK || !strings.Contains(body, wantImage) || !strings.Contains(body, `width="3" height="2"`) || !strings.Contains(body, "3 &times; 2 px") {
		t.Fatalf("picture status=%d is not shown with its size:\n%s", status, body)
	}
	if strings.Contains(body, "This file is not text") {
		t.Error("a picture is still described as a file that cannot be shown")
	}

	body, _ = dashboardGET(t, client, code+"docs%2Fpic.svg")
	if strings.Contains(body, "<img") && strings.Contains(body, "imgview") {
		t.Error("an SVG file is shown as a picture")
	}
	if !strings.Contains(body, "&lt;svg") || !strings.Contains(body, "Download this file") {
		t.Error("an SVG file does not show its source with a download link")
	}

	body, _ = dashboardGET(t, client, code+"fake.png")
	if strings.Contains(body, "imgview") || !strings.Contains(body, "This file is not text") {
		t.Error("a .png file that is not a picture is shown as one")
	}
}

// The repository overview shows the top README of the selected ref, rendered
// by the same helper as the code view, and a repository without one shows
// no README section.
func TestOverviewShowsTheRenderedReadme(t *testing.T) {
	app := newConfiguredApp(t)
	seedRepository(t, app, "with-readme", map[string]string{
		"README.md": hostileReadme,
		"main.go":   "package main\n",
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	seedRepository(t, app, "no-readme", map[string]string{"main.go": "package main\n"}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}

	body, status := dashboardGET(t, client, server.URL+"/repositories/with-readme")
	if status != http.StatusOK || !strings.Contains(body, `<section class="readme"`) || !strings.Contains(body, `<article class="md">`) {
		t.Fatalf("overview status=%d has no rendered README", status)
	}
	readme := body[strings.Index(body, `<section class="readme"`):]
	readme = readme[:strings.Index(readme, "</section>")]
	if !strings.Contains(readme, "Guide") || strings.Contains(readme, "<script") || strings.Contains(readme, "onerror") {
		t.Errorf("overview README is not the safely rendered document:\n%s", readme)
	}
	if !strings.Contains(readme, `href="/repositories/with-readme/code?path=README.md&amp;ref=refs%2Fheads%2Fmain"`) {
		t.Errorf("overview README does not link to the file:\n%s", readme)
	}

	body, _ = dashboardGET(t, client, server.URL+"/repositories/no-readme")
	if strings.Contains(body, `class="readme"`) {
		t.Error("a repository without a README shows a README section")
	}
}

// WebP pictures get their pixel size from the first chunk header, so the
// page reserves their space. The headers are the first 32 bytes of real
// files written by Pillow in its three forms.
func TestWebPSizeFromHeader(t *testing.T) {
	for _, test := range []struct {
		name, header  string
		width, height int
	}{
		{"extended (alpha)", "524946467000000057454250565038580a00000010000000280000160000414c", 41, 23},
		{"lossless", "524946461e000000574542505650384c110000002f248004000750e42ad4a3ff", 37, 19},
		{"lossy", "524946464c0000005745425056503820400000009003009d012a250013003e6d", 37, 19},
		{"not WebP", "89504e470d0a1a0a0000000d4948445200000003000000020806000000000000", 0, 0},
	} {
		head, err := hex.DecodeString(test.header)
		noErr(t, err)
		if width, height := webpSize(head); width != test.width || height != test.height {
			t.Errorf("%s: size %dx%d, want %dx%d", test.name, width, height, test.width, test.height)
		}
	}
	lossy, _ := hex.DecodeString("524946464c0000005745425056503820400000009003009d012a250013003e6d")
	if ok, width, height := inlineImage("pic.webp", lossy); !ok || width != 37 || height != 19 {
		t.Errorf("a WebP picture is shown=%v at %dx%d", ok, width, height)
	}
}
