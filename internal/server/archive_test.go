package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func archiveNames(t *testing.T, format string, content []byte) string {
	t.Helper()
	var names []string
	switch format {
	case "zip":
		reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
		noErr(t, err)
		for _, file := range reader.File {
			names = append(names, file.Name)
		}
	case "tar.gz":
		decompressed, err := gzip.NewReader(bytes.NewReader(content))
		noErr(t, err)
		reader := tar.NewReader(decompressed)
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			noErr(t, err)
			if header.Typeflag != tar.TypeXGlobalHeader {
				names = append(names, header.Name)
			}
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func getArchive(t *testing.T, target string, edits ...func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	noErr(t, err)
	for _, edit := range edits {
		edit(request)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	noErr(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	noErr(t, err)
	return response, body
}

func archiveFixtureRefs(t *testing.T, fixture apiFixture) {
	t.Helper()
	apiRunGit(t, fixture.work, "tag", "v1.0", fixture.sourceOID)
	apiRunGit(t, fixture.work, "push", "-q", "origin", "refs/tags/v1.0", fixture.sourceOID+":refs/heads/topic/login")
	noErr(t, os.MkdirAll(filepath.Join(fixture.work, "docs"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(fixture.work, "docs", "guide.md"), []byte("guide\n"), 0o600))
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-q", "-m", "docs")
	apiRunGit(t, fixture.work, "push", "-q", "origin", "HEAD:refs/heads/docs")
}

func TestArchiveDownloadNamesAndContents(t *testing.T) {
	fixture := newAPIFixture(t, false)
	archiveFixtureRefs(t, fixture)
	server := serve(t, fixture.app.Handler())
	base := server.URL + "/repositories/project/archive?"
	for _, test := range []struct {
		query, filename, names string
	}{
		{"ref=refs%2Fheads%2Fmain&format=zip", "project-main.zip", "project-main/,project-main/file.txt"},
		{"format=zip", "project-main.zip", "project-main/,project-main/file.txt"},
		{"ref=feature&format=tar.gz", "project-feature.tar.gz", "project-feature/,project-feature/feature.txt,project-feature/file.txt"},
		{"ref=refs%2Ftags%2Fv1.0&format=zip", "project-v1.0.zip", "project-v1.0/,project-v1.0/feature.txt,project-v1.0/file.txt"},
		{"ref=refs%2Fheads%2Ftopic%2Flogin&format=zip", "project-topic-login.zip", "project-topic-login/,project-topic-login/feature.txt,project-topic-login/file.txt"},
		{"ref=" + fixture.targetOID + "&format=tar.gz", "project-" + fixture.targetOID + ".tar.gz", "project-" + fixture.targetOID + "/,project-" + fixture.targetOID + "/file.txt"},
	} {
		response, body := getArchive(t, base+test.query)
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Disposition") != "attachment; filename="+test.filename {
			t.Fatalf("%s: status=%d disposition=%q", test.query, response.StatusCode, response.Header.Get("Content-Disposition"))
		}
		format := "zip"
		if strings.HasSuffix(test.filename, ".tar.gz") {
			format = "tar.gz"
		}
		if names := archiveNames(t, format, body); names != test.names {
			t.Fatalf("%s: entries %s, want %s", test.query, names, test.names)
		}
	}

	// Unknown refs, formats, commits, and path tricks are not found.
	for _, target := range []string{
		base + "ref=missing&format=zip",
		base + "ref=main&format=rar",
		base + "ref=main",
		base + "ref=" + strings.Repeat("0", 40) + "&format=zip",
		base + "ref=" + fixture.targetOID[:12] + "&format=zip",
		base + "ref=..%2F..%2Fetc%2Fpasswd&format=zip",
		base + "ref=refs%2Fheads%2F..%2Fmain&format=zip",
		base + "ref=refs%2Fowngit%2Fanything&format=zip",
		server.URL + "/repositories/project/archive/main.zip?format=zip",
		server.URL + "/repositories/missing/archive?ref=main&format=zip",
	} {
		if response, body := getArchive(t, target); response.StatusCode != http.StatusNotFound || response.Header.Get("Content-Disposition") != "" {
			t.Errorf("%s: status=%d, want 404; %.80q", target, response.StatusCode, body)
		}
	}
}

func TestArchiveDownloadNeedsReadAccess(t *testing.T) {
	fixture := newAPIFixture(t, true)
	server := serve(t, fixture.app.Handler())
	browser := server.URL + "/repositories/project/archive?ref=main&format=zip"
	if response, _ := getArchive(t, browser); response.StatusCode != http.StatusSeeOther || !strings.HasPrefix(response.Header.Get("Location"), "/login?next=") {
		t.Fatalf("browser download without a session: status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	if response, _ := getArchive(t, browser, basicAuth("owngit", "shared-password")); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("browser download with Basic credentials: status=%d, want the login redirect", response.StatusCode)
	}
	settings, err := fixture.store.Settings(context.Background())
	noErr(t, err)
	noErr(t, fixture.store.CreateSession(context.Background(), "archive-session", "general", "csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)))
	response, body := getArchive(t, browser, func(request *http.Request) {
		request.AddCookie(&http.Cookie{Name: generalCookie, Value: "archive-session"})
	})
	if response.StatusCode != http.StatusOK || archiveNames(t, "zip", body) != "project-main/,project-main/file.txt" {
		t.Fatalf("browser download with a session: status=%d", response.StatusCode)
	}

	api := server.URL + "/api/v1/repositories/project/archive?ref=main&format=tar.gz"
	for _, attempt := range []struct {
		password, code string
	}{
		{"", "authentication_required"}, {"wrong-password", "invalid_credentials"}, {"admin-password", "invalid_credentials"},
	} {
		response := apiRequest(t, http.MethodGet, api, nil, attempt.password, "")
		if response.StatusCode != http.StatusUnauthorized || apiErrorCode(t, response) != attempt.code {
			t.Errorf("API password %q: status=%d", attempt.password, response.StatusCode)
		}
	}
	// A refused caller cannot tell which repositories exist.
	if response := apiRequest(t, http.MethodGet, server.URL+"/api/v1/repositories/missing/archive?ref=main&format=zip", nil, "", ""); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing repository without a password: status=%d", response.StatusCode)
	}
	response, body = getArchive(t, api, basicAuth("owngit", "shared-password"))
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/gzip" ||
		archiveNames(t, "tar.gz", body) != "project-main/,project-main/file.txt" {
		t.Fatalf("API download: status=%d type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	for _, refused := range []struct {
		target, code string
		status       int
	}{
		{server.URL + "/api/v1/repositories/project/archive?ref=missing&format=zip", "archive_not_found", http.StatusNotFound},
		{server.URL + "/api/v1/repositories/project/archive?ref=main&format=rar", "archive_not_found", http.StatusNotFound},
		{server.URL + "/api/v1/repositories/missing/archive?ref=main&format=zip", "repository_not_found", http.StatusNotFound},
		{server.URL + "/api/v1/repositories/project/archive/main", "not_found", http.StatusNotFound},
		{api + "&path=file.txt", "invalid_request", http.StatusBadRequest},
		{api + "&ref=main", "invalid_request", http.StatusBadRequest},
	} {
		response := apiRequest(t, http.MethodGet, refused.target, nil, "shared-password", "")
		if response.StatusCode != refused.status || apiErrorCode(t, response) != refused.code {
			t.Errorf("%s: status=%d, want %d %s", refused.target, response.StatusCode, refused.status, refused.code)
		}
	}
	if response := apiRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/project/archive", struct{}{}, "shared-password", ""); response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST archive: status=%d", response.StatusCode)
	}
}

// The Code tab offers the branch or tag from its top folder only, and a
// commit page offers that commit.
func TestArchiveLinksOnCodeAndCommitPages(t *testing.T) {
	fixture := newAPIFixture(t, false)
	archiveFixtureRefs(t, fixture)
	server := serve(t, fixture.app.Handler())
	page := func(target string) string {
		response, body := getArchive(t, server.URL+target)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d", target, response.StatusCode)
		}
		return html.UnescapeString(string(body))
	}
	link := func(ref, format string) string {
		return `href="` + archiveURL("project", ref, format) + `"`
	}
	root := page("/repositories/project/code?ref=" + url.QueryEscape("refs/heads/docs"))
	if !strings.Contains(root, link("refs/heads/docs", "zip")) || !strings.Contains(root, link("refs/heads/docs", "tar.gz")) || !strings.Contains(root, `class="dl"`) {
		t.Fatal("the Code tab's top folder does not offer the branch as ZIP and tar.gz")
	}
	if folder := page("/repositories/project/code?ref=" + url.QueryEscape("refs/heads/docs") + "&path=docs"); strings.Contains(folder, "/archive?") {
		t.Fatal("a subfolder offers the whole branch")
	}
	if file := page("/repositories/project/code?ref=" + url.QueryEscape("refs/heads/docs") + "&path=docs%2Fguide.md"); strings.Contains(file, "/archive?") {
		t.Fatal("a file view offers the whole branch")
	}
	if commit := page("/repositories/project/commits/" + fixture.sourceOID); !strings.Contains(commit, link(fixture.sourceOID, "zip")) {
		t.Fatal("the commit page does not offer the commit")
	}
}

func TestArchiveNameKeepsNoPathCharacters(t *testing.T) {
	for ref, want := range map[string]string{
		"main":                   "project-main",
		"feature/login":          "project-feature-login",
		"../../etc":              "project-..-..-etc",
		`a\b:c*d?"e<f>g|h`:       "project-a-b-c-d--e-f-g-h",
		"기능/한글":                  "project-기능-한글",
		"tab\tnew\nline":         "project-tab-new-line",
		strings.Repeat("é", 150): "project-" + strings.Repeat("é", 96),
	} {
		if got := archiveName("project", ref); got != want {
			t.Errorf("archiveName(%q) = %q, want %q", ref, got, want)
		}
	}
}
