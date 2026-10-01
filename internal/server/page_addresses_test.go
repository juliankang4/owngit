package server

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// This opt-in fixture uses real handlers with disposable share authority.
func TestBrowserPageAddressesFixture(t *testing.T) {
	ready := os.Getenv("OWNGIT_PAGE_ADDRESS_BROWSER_READY")
	if ready == "" {
		t.Skip("requires the isolated page-address browser fixture")
	}
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, oid := seedRepository(t, app, "document-addresses", map[string]string{
		"README.md":  "# Original snapshot\n\n" + strings.Repeat("original line\n", 13000),
		"small.txt":  strings.Repeat("small\n", 1000),
		"binary.bin": "binary\x00bytes",
	}, when)
	commitFiles(t, work, map[string]string{"README.md": "# Moved branch\n\n" + strings.Repeat("moved line\n", 13000)}, "move branch", when.Add(time.Hour))
	app.Repositories.Locks.For("document-addresses").Lock()
	app.Repositories.Locks.For("document-addresses").Unlock()
	private := httptest.NewServer(app.Handler())
	t.Cleanup(private.Close)
	public := httptest.NewServer(app.PublicShareHandler())
	t.Cleanup(public.Close)
	created := createShare(t, private.URL, "document-addresses", map[string]any{"label": "Synthetic document addresses"})
	created.URL = public.URL + strings.TrimPrefix(created.URL, private.URL)
	client, home := openShare(t, created)
	address, _ := url.Parse(home)
	secret := ""
	for _, cookie := range client.Jar.Cookies(address) {
		if cookie.Name == shareCookie {
			secret = cookie.Value
		}
	}
	if secret == "" {
		t.Fatal("share authority cookie missing")
	}
	data, err := json.Marshal(map[string]string{
		"owner": private.URL + "/repositories/document-addresses",
		"share": home, "cookie": secret, "sharePath": "/share/" + created.ShareLink.ID, "revision": oid,
	})
	noErr(t, err)
	noErr(t, os.WriteFile(ready, data, 0o600))
	for until := time.Now().Add(5 * time.Minute); time.Now().Before(until); {
		if _, err := os.Stat(ready + ".stop"); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("page-address browser fixture did not finish")
}

func TestPageAddressesRejectIgnoredAndOutOfRangePositions(t *testing.T) {
	app := newConfiguredApp(t)
	work, oid := seedRepository(t, app, "addresses", map[string]string{
		"small.txt":  strings.Repeat("small\n", 1000),
		"binary.bin": "binary\x00bytes",
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	for _, route := range []string{"/code?", "/commits/" + oid + "?"} {
		for _, query := range []string{
			"path=small.txt&from=1001", "path=small.txt&line=1&from=1001",
			"path=small.txt&line=1&from=invalid", "path=small.txt&line=",
			"path=small.txt&from=", "path=small.txt&line=1&from=",
			"path=small.txt&line=invalid&from=1", "path=small.txt&line=1&line=invalid",
			"path=small.txt&line=1&from=1&from=invalid", "path=small.txt&line=%XX",
			"path=binary.bin&line=invalid", "path=binary.bin&from=",
		} {
			body, status := dashboardGET(t, &http.Client{}, server.URL+"/repositories/addresses"+route+query)
			if status != http.StatusBadRequest || !strings.Contains(body, `class="errpage"`) {
				t.Errorf("invalid address %s%s: status=%d, error layout=%v", route, query, status, strings.Contains(body, `class="errpage"`))
			}
		}
	}
	for _, suffix := range []string{"/code?line=1", "/commits?from=1", "/commits/" + oid + "?line=1"} {
		_, status := dashboardGET(t, &http.Client{}, server.URL+"/repositories/addresses"+suffix)
		if status != http.StatusBadRequest {
			t.Errorf("position on a non-line view %s answered %d", suffix, status)
		}
	}
	commitFiles(t, work, map[string]string{"small.txt": "moved branch\n"}, "move branch", time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC))
	app.Repositories.Locks.For("addresses").Lock()
	app.Repositories.Locks.For("addresses").Unlock()
	created := createShare(t, server.URL, "addresses", map[string]any{"label": "Synthetic addresses"})
	shared, home := openShare(t, created)
	for _, viewer := range []struct {
		client *http.Client
		home   string
	}{{&http.Client{}, server.URL + "/repositories/addresses"}, {shared, home}} {
		for _, lang := range []string{"en", "ko"} {
			body, status := dashboardGET(t, viewer.client, viewer.home+"/code?path=small.txt&revision="+oid+"&line=1001&lang="+lang)
			message, label := "This line is not in the file.", "First page"
			if lang == "ko" {
				message, label = "이 파일에는 해당 줄이 없습니다.", "첫 페이지"
			}
			if status != http.StatusNotFound || !strings.Contains(body, message) || !strings.Contains(body, label) || !strings.Contains(body, `class="errpage"`) {
				t.Errorf("absent line (%s): status=%d, expected message/first-page link missing", lang, status)
			}
			link := regexp.MustCompile(`<a class="btn btn--primary" href="([^"]+)"`).FindStringSubmatch(body)
			if link == nil {
				t.Error("missing first-page recovery link")
				continue
			}
			address, _ := url.Parse(html.UnescapeString(link[1]))
			if address.Query().Get("revision") != oid || address.Query().Get("blob") == "" || address.Query().Has("line") || address.Query().Has("from") {
				t.Errorf("recovery link is not the pinned first page: %s", address)
			}
			first, status := dashboardGET(t, viewer.client, server.URL+address.String())
			if status != http.StatusOK || strings.Count(first, `class="codetable__t"`) != 1000 {
				t.Errorf("recovery link status=%d", status)
			}
		}
	}
	_, status := dashboardGET(t, &http.Client{}, server.URL+"/repositories/addresses/commits/"+oid+"?path=small.txt&line=1001")
	if status != http.StatusNotFound {
		t.Errorf("absent selected-diff line answered %d", status)
	}
}

func TestDocumentRepresentationKeepsTheOriginalSnapshot(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, oid := seedRepository(t, app, "document", map[string]string{
		"README.md": "# Original snapshot\n\n" + strings.Repeat("original line\n", 10010),
	}, when)
	commitFiles(t, work, map[string]string{"README.md": "# Moved branch\n"}, "move branch", when.Add(time.Hour))
	app.Repositories.Locks.For("document").Lock()
	app.Repositories.Locks.For("document").Unlock()
	server := serve(t, app.Handler())
	created := createShare(t, server.URL, "document", map[string]any{"label": "Synthetic document"})
	shared, home := openShare(t, created)
	for _, viewer := range []struct {
		client *http.Client
		home   string
	}{{&http.Client{}, server.URL + "/repositories/document"}, {shared, home}} {
		body, status := dashboardGET(t, viewer.client, viewer.home+"/code?path=README.md&revision="+oid+"&view=source&from=10001")
		if status != http.StatusOK || !strings.Contains(body, "original line") {
			t.Fatal("snapshot source fixture did not open")
		}
		for _, source := range []bool{false, true} {
			links := regexp.MustCompile(`<a class="seg__btn" href="([^"]+)"`).FindAllStringSubmatch(body, -1)
			address := ""
			for _, match := range links {
				candidate := html.UnescapeString(match[1])
				if strings.Contains(candidate, "view=source") == source {
					address = candidate
					break
				}
			}
			if address == "" {
				t.Fatal("missing representation link")
			}
			parsed, _ := url.Parse(address)
			if parsed.Query().Get("revision") != oid || parsed.Query().Get("blob") == "" {
				t.Errorf("unpinned representation: %s", address)
			}
			body, status = dashboardGET(t, viewer.client, server.URL+address)
			if status != http.StatusOK || !strings.Contains(body, "Original snapshot") || strings.Contains(body, "Moved branch") {
				t.Errorf("representation changed snapshot (source=%v): status=%d", source, status)
			}
		}
	}
}

func TestDocumentPreviewsExposePinnedSourceLineRouting(t *testing.T) {
	app := newConfiguredApp(t)
	_, oid := seedRepository(t, app, "fragment", map[string]string{
		"README.md": "# Original snapshot\n\n" + strings.Repeat("original line\n", 13000),
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	created := createShare(t, server.URL, "fragment", map[string]any{"label": "Synthetic fragment"})
	shared, home := openShare(t, created)
	for _, viewer := range []struct {
		client *http.Client
		home   string
	}{{&http.Client{}, server.URL + "/repositories/fragment"}, {shared, home}} {
		body, status := dashboardGET(t, viewer.client, viewer.home+"/code?path=README.md#L12345")
		match := regexp.MustCompile(`<article class="md" data-line-page="([^"]+)"`).FindStringSubmatch(body)
		if status != http.StatusOK || match == nil {
			t.Errorf("Markdown preview has no line-routing target: status=%d", status)
			continue
		}
		address, _ := url.Parse(html.UnescapeString(match[1]))
		if address.Query().Get("revision") != oid || address.Query().Get("blob") == "" || address.Query().Get("view") != "source" {
			t.Errorf("preview line target is not pinned source: %s", address)
		}
		query := address.Query()
		query.Set("line", "12345")
		address.RawQuery = query.Encode()
		body, status = dashboardGET(t, viewer.client, server.URL+address.String())
		if status != http.StatusOK || !strings.Contains(body, `id="L12345"`) || strings.Contains(body, `<article class="md"`) {
			t.Errorf("preview line target did not open source: status=%d", status)
		}
	}
}
