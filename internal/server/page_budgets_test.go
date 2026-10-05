package server

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/hostmem"
	"owngit/internal/state"
	"owngit/internal/webui"
)

func TestLinePagesCountWithoutBuildingEveryRow(t *testing.T) {
	content := []byte(strings.Repeat("a\r\n", 1000000))
	lines, total := sourceLinePage(content, 123001)
	if len(lines) != maximumCommitDiffLines || total != 1000000 || lines[0] != "a" {
		t.Fatalf("source: retained=%d total=%d", len(lines), total)
	}
	patch := "@@ -0,0 +1,1000000 @@\n" + strings.Repeat("+a\n", 1000000)
	hunks, total, shown := patchLinePage(patch, 123001, maximumCommitDiffLines)
	if len(hunks) != 1 || total != 1000000 || shown != maximumCommitDiffLines || hunks[0].Lines[0].NewLine != 123001 || hunks[0].Lines[shown-1].NewLine != 133000 {
		t.Fatalf("patch: hunks=%d retained=%d total=%d", len(hunks), shown, total)
	}
}

func TestFileAndSelectedDiffPagesKeepTheirOriginalContent(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	var text strings.Builder
	for i := 1; i <= maximumCommitDiffLines*2+7; i++ {
		fmt.Fprintf(&text, "original %d\n", i)
	}
	work, oid := seedRepository(t, app, "paged", map[string]string{"lines.txt": text.String(), "small.txt": strings.Repeat("small\n", 1000)}, when)
	server := serve(t, app.Handler())
	client := &http.Client{}
	base := server.URL + "/repositories/paged"
	body, status := dashboardGET(t, client, base+"/code?path=lines.txt")
	if status != http.StatusOK || strings.Count(body, `class="codetable__t"`) != maximumCommitDiffLines || !strings.Contains(body, "Showing 10,000 of 20,007") {
		t.Fatalf("first page: status=%d rows=%d", status, strings.Count(body, `class="codetable__t"`))
	}
	more := nextPageAddress(t, body)
	if !strings.Contains(more, "revision="+oid) || !strings.Contains(more, "blob=") {
		t.Fatalf("unpinned continuation %q", more)
	}
	commitFiles(t, work, map[string]string{"lines.txt": "after push\n"}, "move main", when.Add(time.Hour))
	app.Repositories.Locks.For("paged").Lock()
	app.Repositories.Locks.For("paged").Unlock()
	body, status = dashboardGET(t, client, server.URL+more)
	if status != http.StatusOK || !strings.Contains(body, `id="L10001"`) || !strings.Contains(body, "original 10001") || strings.Contains(body, "after push") || !strings.Contains(body, "Download the file at the current ref") {
		t.Fatalf("second page did not retain its blob: status=%d", status)
	}
	body, status = dashboardGET(t, client, server.URL+nextPageAddress(t, body))
	if status != http.StatusOK || strings.Count(body, `class="codetable__t"`) != 7 || !strings.Contains(body, "original 20007") || strings.Contains(body, `rel="next"`) {
		t.Fatalf("last page: status=%d", status)
	}
	body, status = dashboardGET(t, client, base+"/code?path=lines.txt&revision="+oid+"&line=12345#L12345")
	if status != http.StatusOK || !strings.Contains(body, `id="L12345"`) || strings.Contains(body, `id="L1"`) {
		t.Fatalf("script-free line address: status=%d", status)
	}
	body, status = dashboardGET(t, client, base+"/code?path=small.txt")
	if status != http.StatusOK || strings.Count(body, `class="codetable__t"`) != 1000 || strings.Contains(body, "data-page-continuation") {
		t.Fatalf("small file changed: status=%d", status)
	}
	for _, from := range []int{1, 10001, 20001} {
		body, status = dashboardGET(t, client, base+"/commits/"+oid+"?path=lines.txt&from="+strconv.Itoa(from))
		want := min(maximumCommitDiffLines, 20008-from)
		if status != http.StatusOK || strings.Count(body, `class="difftable__r is-add"`) != want || !strings.Contains(body, "original "+strconv.Itoa(from)) {
			t.Fatalf("selected diff from=%d status=%d rows=%d", from, status, strings.Count(body, `class="difftable__r is-add"`))
		}
	}
	for _, suffix := range []string{"&line=0", "&line=no", "&from=999999", "&line=999999999999999999999999999"} {
		_, status = dashboardGET(t, client, base+"/code?path=small.txt"+suffix)
		if status != http.StatusBadRequest {
			t.Fatalf("invalid page %q answered %d", suffix, status)
		}
	}
	remote, _ := app.Repositories.Path("paged")
	apiRunGit(t, remote, "update-ref", "-d", "refs/heads/main")
	app.Repositories.Locks.For("paged").Lock()
	app.Repositories.Locks.For("paged").Unlock()
	body, status = dashboardGET(t, client, base+"/code?path=lines.txt&revision="+oid+"&line=12345")
	if status != http.StatusOK || !strings.Contains(body, `id="L12345"`) || !strings.Contains(body, "original 12345") {
		t.Fatalf("owner continuation lost its commit after branch deletion: status=%d", status)
	}
	body, status = dashboardGET(t, client, base+"/commits/"+oid+"?path=lines.txt&from=20001")
	if status != http.StatusOK || strings.Count(body, `class="difftable__r is-add"`) != 7 {
		t.Fatalf("diff continuation lost its commit after branch deletion: status=%d", status)
	}
}

func TestByteLimitedLinePagesDoNotClaimTheWholeFileCount(t *testing.T) {
	app := newConfiguredApp(t)
	seedRepository(t, app, "byte-limited", map[string]string{"lines.txt": strings.Repeat("a\n", 1100000)}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	body, status := dashboardGET(t, &http.Client{}, server.URL+"/repositories/byte-limited/code?path=lines.txt")
	if status != http.StatusOK || !strings.Contains(body, "loaded lines") || !strings.Contains(body, "outside the display size limit") || !strings.Contains(body, "Only the beginning") || !strings.Contains(body, "Download this file") {
		t.Fatalf("byte-limited count is not identified as a prefix: status=%d", status)
	}
	if strings.Contains(body, "of 1,100,000") {
		t.Fatal("prefix page claimed a complete line count")
	}
	body, status = dashboardGET(t, &http.Client{}, server.URL+"/repositories/byte-limited/code?path=lines.txt&line=1050000")
	if status != http.StatusBadRequest || strings.Contains(body, "This line is not in the file.") {
		t.Fatalf("an unloaded line was claimed absent from the file: status=%d", status)
	}
}

// A file above the memory bound of one Git read is shown as too large, not as
// the beginning of a text file: no lines, no line address refusal, and no
// download link even when the saved download limit would allow the download.
func TestFileAboveTheReadBoundShowsOneTooLargeNotice(t *testing.T) {
	saved := hostmem.Ceiling
	hostmem.Ceiling = func() uint64 { return 512 << 20 }
	defer func() { hostmem.Ceiling = saved }()

	app := newConfiguredApp(t)
	// The download limit is raised above the bound, so only the read bound
	// refuses a download the page must not offer.
	limits := state.DefaultBrowseLimits
	limits.RawBytes = state.MaximumRawBytes
	noErr(t, app.Store.SavePolicies(context.Background(), state.PolicyChange{Browse: &limits}))
	bound := app.Repositories.Git.ReadBound()
	if bound == 0 {
		t.Fatal("the forced ceiling gave no read bound")
	}
	seedRepository(t, app, "bound-page", map[string]string{
		"big.txt":   strings.Repeat("x", int(bound)+1),
		"small.txt": "small file\n",
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	client := &http.Client{}
	base := server.URL + "/repositories/bound-page/code?ref=refs%2Fheads%2Fmain&path="
	notice := browserText(webui.MsgCodeTooLarge)

	body, status := dashboardGET(t, client, base+"big.txt")
	if status != http.StatusOK || !strings.Contains(body, notice) || !strings.Contains(body, webui.Text(webui.LangKO, webui.MsgCodeTooLarge)) {
		t.Fatalf("a file above the read bound: status=%d", status)
	}
	for _, unwanted := range []string{
		"Only the beginning of this file is shown", "This file is not text", `class="codetable"`,
		"larger than the raw file download limit", "/raw?", strings.Repeat("x", 64),
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the too-large page contains %q", unwanted)
		}
	}
	// A line address names no line, and the page still says the file is too
	// large instead of refusing the address.
	line, status := dashboardGET(t, client, base+"big.txt&line=5")
	if status != http.StatusOK || !strings.Contains(line, notice) {
		t.Fatalf("a line address to a too-large file: status=%d", status)
	}
	// The raw address is refused for the same reason, not by the saved limit.
	raw := browserGET(t, client, server.URL+"/repositories/bound-page/raw?ref=refs%2Fheads%2Fmain&path=big.txt")
	if raw.status != http.StatusForbidden || !strings.Contains(raw.body, notice) || strings.Contains(raw.body, "raw file download limit") {
		t.Fatalf("raw download of a too-large file: status=%d", raw.status)
	}
	// A small file is unchanged.
	small, status := dashboardGET(t, client, base+"small.txt")
	if status != http.StatusOK || !strings.Contains(small, "small file") || !strings.Contains(small, "/raw?") || strings.Contains(small, notice) {
		t.Fatalf("the small control file changed: status=%d", status)
	}
}

func nextPageAddress(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`<a class="btn" href="([^"]+)" rel="next">`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("no continuation link")
	}
	return html.UnescapeString(match[1])
}

func TestCodePagePinsDoNotExposeKeptHistoryToShares(t *testing.T) {
	app := newConfiguredApp(t)
	work, oid := seedRepository(t, app, "restricted", map[string]string{"keep.txt": "before\n"}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	apiRunGit(t, work, "checkout", "--orphan", "unrelated")
	apiRunGit(t, work, "commit", "-q", "-m", "unrelated")
	other := apiGitOutput(t, work, "rev-parse", "HEAD")
	page := webui.RepositoryPage{Shared: true, Repo: webui.RepositoryHeader{ID: "restricted"}, Ref: webui.RefSelection{Revision: oid}}
	request, _ := http.NewRequest(http.MethodGet, "http://example.invalid/code?revision="+other, nil)
	// Publish the detached object but not a public branch that contains it.
	remote, _ := app.Repositories.Path("restricted")
	apiRunGit(t, work, "push", "-q", remote, "HEAD:refs/heads/temporary")
	apiRunGit(t, remote, "update-ref", "refs/owngit/test-only", other)
	apiRunGit(t, remote, "update-ref", "-d", "refs/heads/temporary")
	if _, err := app.codePageRevision(request.WithContext(context.Background()), &page, other); err == nil {
		t.Fatal("share continuation exposed an unrelated commit")
	}
}
