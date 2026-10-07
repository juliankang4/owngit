package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNamedFilesRemainReadableBesideOversizedSiblings(t *testing.T) {
	app := newConfiguredApp(t)
	picture, err := base64.StdEncoding.DecodeString("R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7")
	noErr(t, err)
	_, parent := seedRepository(t, app, "single-boundary", map[string]string{
		"small.txt": "x\n", "README.md": "# Named document\n", "binary.bin": "binary\x00bytes", "pixel.gif": string(picture),
	}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	remote, err := app.Repositories.Path("single-boundary")
	noErr(t, err)
	var entries strings.Builder
	for _, name := range []string{"small.txt", "README.md", "binary.bin", "pixel.gif"} {
		fmt.Fprintf(&entries, "100644 blob %s\t%s\x00", apiGitOutput(t, remote, "rev-parse", parent+":"+name), name)
	}
	blob := apiGitOutput(t, remote, "rev-parse", parent+":small.txt")
	server := serve(t, app.Handler())
	created := createShare(t, server.URL, "single-boundary", map[string]any{"label": "Synthetic single-path boundary"})
	shared, home := openShare(t, created)
	for _, character := range []string{"a", "x"} {
		tree, err := app.Repositories.Git.Run(context.Background(), remote, strings.NewReader(entries.String()+fmt.Sprintf("100644 blob %s\t%s\x00", blob, strings.Repeat(character, 4097))), "--git-dir", ".", "mktree", "-z")
		noErr(t, err)
		commit, err := app.Repositories.Git.Run(context.Background(), remote, strings.NewReader("Synthetic mixed tree\n"), "--git-dir", ".", "-c", "user.name=Page Test", "-c", "user.email=page@example.invalid", "commit-tree", strings.TrimSpace(string(tree.Stdout)), "-p", parent)
		noErr(t, err)
		apiRunGit(t, remote, "update-ref", "refs/heads/main", strings.TrimSpace(string(commit.Stdout)))
		wroteRefs(app, "single-boundary")
		for _, viewer := range []struct {
			role   string
			client *http.Client
			home   string
		}{{"owner", &http.Client{}, server.URL + "/repositories/single-boundary"}, {"share", shared, home}} {
			t.Run(character+"/"+viewer.role, func(t *testing.T) {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					request, err := http.NewRequest(method, viewer.home+"/raw?path=small.txt", nil)
					noErr(t, err)
					response, err := viewer.client.Do(request)
					noErr(t, err)
					body, err := io.ReadAll(response.Body)
					response.Body.Close()
					noErr(t, err)
					want := "x\n"
					if method == http.MethodHead {
						want = ""
					}
					t.Logf("raw %s: status=%d body-bytes=%d content-length=%d", method, response.StatusCode, len(body), response.ContentLength)
					if response.StatusCode != http.StatusOK || string(body) != want || response.ContentLength != 2 || response.Header.Get("Content-Disposition") != "attachment; filename=small.txt" {
						t.Errorf("raw %s: status=%d bytes=%d length=%d disposition=%q", method, response.StatusCode, len(body), response.ContentLength, response.Header.Get("Content-Disposition"))
					}
				}
				for _, lang := range []string{"en", "ko"} {
					for _, view := range []struct{ query, content string }{
						{"path=small.txt", `class="codetable__t">x</td>`},
						{"path=small.txt&line=1", `id="L1"`},
						{"path=README.md", `<h1 id="md-named-document">Named document</h1>`},
						{"path=README.md&view=source", `class="codetable__t"># Named document</td>`},
						{"path=README.md&line=1", `id="L1"`},
						{"path=binary.bin", `class="empty"`},
						{"path=pixel.gif", `class="imgview__img"`},
					} {
						body, status := dashboardGET(t, viewer.client, viewer.home+"/code?"+view.query+"&lang="+lang)
						if status != http.StatusOK || !strings.Contains(body, view.content) {
							t.Errorf("%s %s: status=%d expected file content missing", lang, view.query, status)
							continue
						}
						if view.query == "path=binary.bin" {
							message := "This file is not text, so it is not shown here."
							if lang == "ko" {
								message = "이 파일은 텍스트가 아니어서 여기에 표시하지 않습니다."
							}
							if !strings.Contains(body, message) || strings.Contains(body, `class="codetable__t"`) {
								t.Error("binary file facts missing")
							}
						}
						start := strings.Index(body, `<nav class="tree"`)
						if start < 0 {
							t.Error("file drawer missing")
							continue
						}
						end := strings.Index(body[start:], "</nav>")
						if end < 0 {
							t.Fatal("file drawer did not close")
						}
						drawer := body[start : start+end]
						unavailable := "This is temporarily unavailable."
						if lang == "ko" {
							unavailable = "지금은 사용할 수 없습니다."
						}
						if !strings.Contains(drawer, unavailable) || strings.Contains(drawer, "tree__row") || strings.Contains(drawer, "data-page-continuation") || strings.Contains(drawer, "page-transfer-complete") {
							t.Errorf("%s %s: drawer is not honestly unavailable: %s", lang, view.query, drawer)
						}
						if strings.Count(body, "data-page-transfer-complete") != 1 || strings.Index(body, "data-page-transfer-complete") < start+end || !strings.Contains(body, `<summary class="btn drawer__btn">`) {
							t.Error("document completion or native drawer toggle changed")
						}
					}
					body, status := dashboardGET(t, viewer.client, viewer.home+"/code?lang="+lang)
					if status != http.StatusServiceUnavailable || strings.Contains(body, "This folder is empty.") || strings.Contains(body, "data-page-continuation") {
						t.Errorf("folder must remain unavailable: lang=%s status=%d", lang, status)
					}
				}
			})
		}
	}
	if ready := os.Getenv("OWNGIT_FILE_DRAWER_BROWSER_READY"); ready != "" {
		public := httptest.NewServer(app.PublicShareHandler())
		t.Cleanup(public.Close)
		created.URL = public.URL + strings.TrimPrefix(created.URL, server.URL)
		client, home := openShare(t, created)
		address, _ := url.Parse(home)
		secret := ""
		for _, cookie := range client.Jar.Cookies(address) {
			if cookie.Name == shareCookie+"_http" {
				secret = cookie.Value
			}
		}
		if secret == "" {
			t.Fatal("disposable share authority missing")
		}
		data, err := json.Marshal(map[string]string{
			"owner": server.URL + "/repositories/single-boundary", "share": home,
			"cookie": secret, "sharePath": "/share/" + created.ShareLink.ID,
		})
		noErr(t, err)
		noErr(t, os.WriteFile(ready, data, 0o600))
		for until := time.Now().Add(3 * time.Minute); time.Now().Before(until); {
			if _, err := os.Stat(ready + ".stop"); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("file drawer browser fixture did not finish")
	}
}
