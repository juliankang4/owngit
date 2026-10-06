package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"owngit/internal/state"
)

// seedHistory writes one commit per name in subjects (oldest first) to
// refs/heads/main, each changing files like the matching entry of files, with
// git fast-import so large histories stay cheap.
func seedHistory(t *testing.T, app *App, id string, subjects []string, files map[int][]string) {
	t.Helper()
	if _, err := app.Repositories.Create(context.Background(), id, ""); err != nil {
		t.Fatal(err)
	}
	path, _ := app.Repositories.Path(id)
	var stream strings.Builder
	for index, subject := range subjects {
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter S <s@example.invalid> %d +0000\ndata %d\n%s\n", 1700000000+index, len(subject), subject)
		if len(files[index]) == 0 {
			stream.WriteString("M 100644 inline README.md\ndata 2\nx\n\n")
		}
		for _, name := range files[index] {
			fmt.Fprintf(&stream, "M 100644 inline %s\ndata 2\nx\n\n", name)
		}
	}
	command := exec.Command("git", "--git-dir", path, "fast-import", "--quiet")
	command.Stdin = strings.NewReader(stream.String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, output)
	}
	wroteRefs(app, id)
}

// get reads target, asking for gzip when compress is set, with no automatic
// decoding so the test sees what is on the wire.
func get(t *testing.T, target string, header http.Header) (*http.Response, []byte) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, target, nil)
	for key, values := range header {
		request.Header[key] = values
	}
	response, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(request)
	noErr(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	noErr(t, err)
	return response, body
}

var olderLink = regexp.MustCompile(`class="btn" href="([^"]*skip=[^"]*)"><span[^>]*>Older<`)

func TestCommitListPagesToOlderCommits(t *testing.T) {
	app := newConfiguredApp(t)
	var subjects []string
	for index := 0; index < 230; index++ {
		subjects = append(subjects, fmt.Sprintf("commit-%04d", index))
	}
	seedHistory(t, app, "history", subjects, nil)
	server := serve(t, app.Handler())
	base := server.URL + "/repositories/history/commits"

	_, body := get(t, base, nil)
	first := string(body)
	if !strings.Contains(first, "commit-0229") || !strings.Contains(first, "commit-0130") || strings.Contains(first, "commit-0129") {
		t.Fatal("first page does not hold exactly the newest 100 commits")
	}
	link := olderLink.FindStringSubmatch(first)
	if link == nil {
		t.Fatal("first page has no link to older commits")
	}
	_, body = get(t, server.URL+html.UnescapeString(link[1]), nil)
	second := string(body)
	if !strings.Contains(second, "commit-0129") || !strings.Contains(second, "commit-0030") || strings.Contains(second, "commit-0130") || strings.Contains(second, "commit-0029") {
		t.Fatal("second page does not continue with the next 100 commits")
	}
	link = olderLink.FindStringSubmatch(second)
	if link == nil {
		t.Fatal("second page has no link to older commits")
	}
	_, body = get(t, server.URL+html.UnescapeString(link[1]), nil)
	last := string(body)
	if !strings.Contains(last, "commit-0029") || !strings.Contains(last, "commit-0000") || olderLink.MatchString(last) {
		t.Fatal("last page is not the 30 oldest commits without an older link")
	}
	// A root that is not in the selected history is not a page start.
	if response, _ := get(t, base+"?skip=100&root="+strings.Repeat("0", 40), nil); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown root status=%d", response.StatusCode)
	}
}

// Both sides of a merge cross the page boundary, so the pages must continue
// one traversal: restarting from the first commit of a page would lose the
// merged branch's commits that are still to come.
func TestCommitListPagesKeepMergedHistory(t *testing.T) {
	app := newConfiguredApp(t)
	if _, err := app.Repositories.Create(context.Background(), "merged", ""); err != nil {
		t.Fatal(err)
	}
	path, _ := app.Repositories.Path("merged")
	var stream strings.Builder
	commit := func(ref, mark, subject string, when int, from, merge string) {
		fmt.Fprintf(&stream, "commit %s\nmark :%s\ncommitter S <s@example.invalid> %d +0000\ndata %d\n%s\n", ref, mark, when, len(subject), subject)
		if from != "" {
			fmt.Fprintf(&stream, "from :%s\n", from)
		}
		if merge != "" {
			fmt.Fprintf(&stream, "merge :%s\n", merge)
		}
		fmt.Fprintf(&stream, "M 100644 inline f-%s.txt\ndata 2\nx\n\n", mark)
	}
	commit("refs/heads/main", "1", "root", 1700000000, "", "")
	left, right := "1", "1"
	// The two sides alternate in time, so the merged order interleaves them.
	for index := 0; index < 110; index++ {
		mark := fmt.Sprint(10 + index)
		commit("refs/heads/left", mark, fmt.Sprintf("left-%03d", index), 1700000100+index*2, left, "")
		left = mark
		mark = fmt.Sprint(1000 + index)
		commit("refs/heads/right", mark, fmt.Sprintf("right-%03d", index), 1700000101+index*2, right, "")
		right = mark
	}
	commit("refs/heads/main", "9999", "merge", 1700001000, left, right)
	command := exec.Command("git", "--git-dir", path, "fast-import", "--quiet")
	command.Stdin = strings.NewReader(stream.String())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, output)
	}
	wroteRefs(app, "merged")
	server := serve(t, app.Handler())

	seen := map[string]bool{}
	target := server.URL + "/repositories/merged/commits"
	for pages := 0; target != ""; pages++ {
		if pages > 5 {
			t.Fatal("pages do not end")
		}
		_, body := get(t, target, nil)
		for _, found := range regexp.MustCompile(`(left|right)-\d{3}`).FindAllString(string(body), -1) {
			seen[found] = true
		}
		target = ""
		if link := olderLink.FindStringSubmatch(string(body)); link != nil {
			target = server.URL + html.UnescapeString(link[1])
		}
	}
	if len(seen) != 220 {
		t.Fatalf("pages listed %d of the 220 side commits", len(seen))
	}
}

func TestLargeChangeShowsBoundedFileSectionsWithPaging(t *testing.T) {
	app := newConfiguredApp(t)
	var names []string
	for index := 0; index < maximumDiffFiles+20; index++ {
		names = append(names, fmt.Sprintf("f%04d.txt", index))
	}
	seedHistory(t, app, "wide", []string{"base", "many files"}, map[int][]string{1: names})
	repositoryPath, _ := app.Repositories.Path("wide")
	oid := apiGitOutput(t, repositoryPath, "rev-parse", "refs/heads/main")
	server := serve(t, app.Handler())
	page := server.URL + "/repositories/wide/commits/" + strings.TrimSpace(oid)

	_, body := get(t, page, nil)
	first := string(body)
	next := regexp.MustCompile(`class="btn" href="([^"]*files=[^"]*)"`).FindStringSubmatch(first)
	if !strings.Contains(first, "f0399.txt") || strings.Contains(first, "f0400.txt") || next == nil {
		t.Fatal("first page does not hold the first file sections with a link to the rest")
	}
	_, body = get(t, server.URL+html.UnescapeString(next[1]), nil)
	if rest := string(body); !strings.Contains(rest, "f0400.txt") || !strings.Contains(rest, "f0419.txt") || strings.Contains(rest, "f0399.txt") {
		t.Fatal("second page does not hold the remaining file sections")
	}
}

// Pages carry the session's form token and repeat what the request carried,
// so they are never compressed. Static assets are.
func TestOnlyStaticAssetsAreCompressed(t *testing.T) {
	app := newConfiguredApp(t)
	if _, err := app.Repositories.Create(context.Background(), "zipped-sample", ""); err != nil {
		t.Fatal(err)
	}
	server := serve(t, app.Handler())
	accept := http.Header{"Accept-Encoding": {"gzip"}}

	for _, target := range []string{"/", "/?q=zipped-sample", "/?q=%20"} {
		if response, _ := get(t, server.URL+target, accept); response.Header.Get("Content-Encoding") != "" {
			t.Fatalf("page %q was compressed", target)
		}
	}
	response, body := get(t, server.URL+"/assets/owngit.css", accept)
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if response.Header.Get("Content-Encoding") != "gzip" || err != nil {
		t.Fatal("stylesheet not compressed")
	}
	if plain, _ := io.ReadAll(reader); !strings.Contains(string(plain), "@font-face") {
		t.Fatal("compressed stylesheet lost its content")
	}
	if response, _ = get(t, server.URL+"/assets/owngit.css", nil); response.Header.Get("Content-Encoding") != "" {
		t.Fatal("a client that did not ask for gzip got it")
	}
}

// The font is named by the stylesheet without a version, so a browser asks
// again when its short lifetime ends and must be told nothing changed.
func TestUnversionedAssetRevalidates(t *testing.T) {
	app := newConfiguredApp(t)
	server := serve(t, app.Handler())
	font := server.URL + "/assets/fonts/PretendardVariable.woff2"
	response, body := get(t, font, nil)
	tag := response.Header.Get("ETag")
	if response.StatusCode != http.StatusOK || tag == "" || len(body) == 0 {
		t.Fatalf("font status=%d etag=%q", response.StatusCode, tag)
	}
	response, body = get(t, font, http.Header{"If-None-Match": {tag}})
	if response.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("revalidation status=%d body=%d", response.StatusCode, len(body))
	}
}

// importStream creates repository id from a git fast-import stream.
func importStream(t *testing.T, app *App, id, stream string) {
	t.Helper()
	if _, err := app.Repositories.Create(context.Background(), id, ""); err != nil {
		t.Fatal(err)
	}
	path, _ := app.Repositories.Path(id)
	command := exec.Command("git", "--git-dir", path, "fast-import", "--quiet")
	command.Stdin = strings.NewReader(stream)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, output)
	}
	wroteRefs(app, id)
}

// A later page of a long pull request lists its own files with their
// changes, and a file can be opened alone, as the page for a new request and
// for a recorded one both do through the same view.
func TestLaterPullRequestPageLoadsItsChanges(t *testing.T) {
	t.Run("a file longer than a page", func(t *testing.T) { singleLongFileOfAPullRequest(t) })
	t.Run("a file that became a folder", func(t *testing.T) { singleFileThatBecameAFolder(t) })
	t.Run("a file the compare limit cuts from the list", func(t *testing.T) { singleFileBeyondTheCompareLimit(t) })
	app := newConfiguredApp(t)
	var stream strings.Builder
	stream.WriteString("commit refs/heads/main\ncommitter S <s@example.invalid> 1700000000 +0000\ndata 4\nbase\nM 100644 inline base.txt\ndata 2\nx\n\n")
	stream.WriteString("commit refs/heads/many\ncommitter S <s@example.invalid> 1700000100 +0000\ndata 4\nmany\nfrom refs/heads/main\n")
	const files = 2400
	for index := 0; index < files; index++ {
		fmt.Fprintf(&stream, "M 100644 inline f%04d.txt\ndata 2\nx\n\n", index)
	}
	importStream(t, app, "wide-pr", stream.String())
	server := serve(t, app.Handler())
	page := server.URL + "/repositories/wide-pr/pull-requests/new?source=many&target=main"

	_, body := get(t, page+"&files=2001", nil)
	later := string(body)
	if !strings.Contains(later, "f2001.txt") || !strings.Contains(later, "f2399.txt") || strings.Contains(later, "f0000.txt") {
		t.Fatal("later page does not list its own files")
	}
	if strings.Contains(later, "was not loaded") || !strings.Contains(later, "difftable") {
		t.Fatal("later page lists files without their changes")
	}
	_, body = get(t, page+"&file=f1234.txt", nil)
	if one := string(body); !strings.Contains(one, "f1234.txt") || strings.Contains(one, "f1235.txt") || !strings.Contains(one, "difftable") {
		t.Fatal("one file is not shown alone")
	}
}

// A file with more changed lines than a page shows is one file: its line
// pages never count as pages of files.
func TestSingleFileLinePagesDoNotCountFiles(t *testing.T) {
	app := newConfiguredApp(t)
	var stream strings.Builder
	content := strings.Repeat("line\n", maximumCommitDiffLines+1)
	fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter S <s@example.invalid> 1700000000 +0000\ndata 4\nbig\nM 100644 inline big.txt\ndata %d\n%s\n", len(content), content)
	importStream(t, app, "tall", stream.String())
	repositoryPath, _ := app.Repositories.Path("tall")
	oid := strings.TrimSpace(apiGitOutput(t, repositoryPath, "rev-parse", "refs/heads/main"))
	server := serve(t, app.Handler())
	_, body := get(t, server.URL+"/repositories/tall/commits/"+oid+"?path=big.txt", nil)
	if page := string(body); !strings.Contains(page, "1 file") || strings.Contains(page, "10,001 files") {
		t.Fatal("the line total of one file was shown as a number of files")
	}
}

// A file with more changed lines than a page shows is read alone and paged by
// lines, as a single-file commit diff is, when its link is followed.
func singleLongFileOfAPullRequest(t *testing.T) {
	app := newConfiguredApp(t)
	content := strings.Repeat("line\n", maximumCommitDiffLines+1)
	var stream strings.Builder
	stream.WriteString("commit refs/heads/main\ncommitter S <s@example.invalid> 1700000000 +0000\ndata 4\nbase\nM 100644 inline base.txt\ndata 2\nx\n\n")
	fmt.Fprintf(&stream, "commit refs/heads/tall\ncommitter S <s@example.invalid> 1700000100 +0000\ndata 4\ntall\nfrom refs/heads/main\nM 100644 inline big.txt\ndata %d\n%s\n", len(content), content)
	importStream(t, app, "tall-pr", stream.String())
	server := serve(t, app.Handler())
	page := server.URL + "/repositories/tall-pr/pull-requests/new?source=tall&target=main"

	_, body := get(t, page, nil)
	first := regexp.MustCompile(`href="([^"]*file=big[^"]*)"`).FindStringSubmatch(string(body))
	if first == nil {
		t.Fatal("the not-loaded file has no link to its own view")
	}
	_, body = get(t, server.URL+html.UnescapeString(first[1]), nil)
	one := string(body)
	more := regexp.MustCompile(`class="btn" href="([^"]*from=[^"]*)"`).FindStringSubmatch(one)
	if strings.Contains(one, "was not loaded") || !strings.Contains(one, "difftable") || more == nil || !strings.Contains(one, "1 file") || strings.Contains(one, "10,001 files") {
		t.Fatal("the single file view does not load the file's first lines with a link to the rest")
	}
	_, body = get(t, server.URL+html.UnescapeString(more[1]), nil)
	if rest := string(body); !strings.Contains(rest, "difftable") || strings.Contains(rest, "was not loaded") {
		t.Fatal("the rest of the file's lines is not shown")
	}
}

// The view of one file reads that path on its own: a long list of changed
// files that fills the compare limit must not decide what it shows.
func singleFileBeyondTheCompareLimit(t *testing.T) {
	app := newConfiguredApp(t)
	browse := state.DefaultBrowseLimits
	browse.CompareBytes = state.MinimumBrowseBytes
	noErr(t, app.Store.SavePolicies(context.Background(), state.PolicyChange{Browse: &browse}))
	var stream strings.Builder
	stream.WriteString("commit refs/heads/main\ncommitter S <s@example.invalid> 1700000000 +0000\ndata 4\nbase\nM 100644 inline base.txt\ndata 2\nx\n\n")
	stream.WriteString("commit refs/heads/long\ncommitter S <s@example.invalid> 1700000100 +0000\ndata 4\nlong\nfrom refs/heads/main\n")
	padding := strings.Repeat("d", 150)
	for index := 0; index < 800; index++ {
		fmt.Fprintf(&stream, "M 100644 inline a%04d-%s.txt\ndata 2\nx\n\n", index, padding)
	}
	stream.WriteString("M 100644 inline zzz-target.txt\ndata 2\nq\n\n")
	importStream(t, app, "long-list", stream.String())
	server := serve(t, app.Handler())
	_, body := get(t, server.URL+"/repositories/long-list/pull-requests/new?source=long&target=main&file=zzz-target.txt", nil)
	one := string(body)
	if !strings.Contains(one, "difftable") || !strings.Contains(one, "zzz-target.txt") || strings.Contains(one, "No line changes") {
		t.Fatal("a file after the cut of a long list is not read on its own")
	}
}

// A path names what is below it as well, but the view of one file shows
// that path only: a file replaced by a folder is the deleted file, not the
// folder's new file.
func singleFileThatBecameAFolder(t *testing.T) {
	app := newConfiguredApp(t)
	stream := "commit refs/heads/main\ncommitter S <s@example.invalid> 1700000000 +0000\ndata 4\nbase\nM 100644 inline node\ndata 4\nold\n\n" +
		"commit refs/heads/folder\ncommitter S <s@example.invalid> 1700000100 +0000\ndata 4\nnext\nfrom refs/heads/main\nD node\nM 100644 inline node/child.txt\ndata 6\nchild\n\n"
	importStream(t, app, "became-folder", stream)
	server := serve(t, app.Handler())
	_, body := get(t, server.URL+"/repositories/became-folder/pull-requests/new?source=folder&target=main&file=node", nil)
	one := string(body)
	if !strings.Contains(one, "1 file") || strings.Contains(one, "child.txt") || strings.Contains(one, "+child") {
		t.Fatal("the view of a file shows the files below its path")
	}
}
