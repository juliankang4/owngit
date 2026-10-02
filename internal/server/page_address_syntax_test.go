package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPageSyntaxPreservesMaximumCursorBytes(t *testing.T) {
	cursor := "f:" + strings.Repeat("x", 4094) + "\xfe\xff"
	request, err := http.NewRequest(http.MethodGet, "http://example.invalid/code?after="+url.QueryEscape(cursor)+"&revision="+strings.Repeat("a", 64)+"&blob="+strings.Repeat("b", 64), nil)
	noErr(t, err)
	address, err := parsePageAddress(request, []string{"code"})
	if err != nil || address.After != cursor || len(address.Revision) != 64 || len(address.Blob) != 64 {
		t.Fatalf("maximum cursor/sha256 syntax: %v", err)
	}
}

func TestPageSyntaxIsRejectedBeforeGitOnOwnerAndShareRoutes(t *testing.T) {
	app := newConfiguredApp(t)
	_, oid := seedRepository(t, app, "syntax", map[string]string{"small.txt": "small\n"}, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC))
	server := serve(t, app.Handler())
	created := createShare(t, server.URL, "syntax", map[string]any{"label": "Synthetic syntax"})
	shared, home := openShare(t, created)
	counts := countGit(t, app)
	queries := []string{"line=", "line=invalid", "from=", "line=1&from=bad", "line=1&line=2", "revision=", "revision=main", "revision=" + oid + "&revision=" + oid, "revision=" + strings.Repeat("a", 10000), "blob=", "blob=main", "blob=" + oid + "&blob=" + oid, "blob=" + strings.Repeat("a", 10000), "after=", "after=x:name", "after=f:", "after=f:a/b", "after=f:" + url.QueryEscape(strings.Repeat("x", 4097)), "after=f:a&after=f:b"}
	for _, viewer := range []struct {
		client *http.Client
		home   string
	}{{&http.Client{}, server.URL + "/repositories/syntax"}, {shared, home}} {
		for _, query := range queries {
			wroteRefs(app, "syntax")
			counts()
			body, status := dashboardGET(t, viewer.client, viewer.home+"/code?path=small.txt&"+query)
			commands := counts()
			if status != http.StatusBadRequest || !strings.Contains(body, `class="errpage"`) || len(commands) != 0 {
				t.Errorf("syntax %s: status=%d Git=%q", strings.SplitN(query, "=", 2)[0], status, commands)
			}
		}
		for _, commit := range []string{"invalid", strings.Repeat("a", 10000)} {
			wroteRefs(app, "syntax")
			counts()
			_, status := dashboardGET(t, viewer.client, viewer.home+"/commits/"+commit)
			if commands := counts(); status != http.StatusBadRequest || len(commands) != 0 {
				t.Errorf("commit syntax: status=%d Git=%q", status, commands)
			}
		}
	}
	// A reachable tree outside the path contract is unavailable, not empty.
	remote, err := app.Repositories.Path("syntax")
	noErr(t, err)
	blob := apiGitOutput(t, remote, "rev-parse", oid+":small.txt")
	tree, err := app.Repositories.Git.Run(context.Background(), remote, strings.NewReader("100644 blob "+blob+"\t"+strings.Repeat("x", 4097)+"\x00"), "--git-dir", ".", "mktree", "-z")
	noErr(t, err)
	commit, err := app.Repositories.Git.Run(context.Background(), remote, strings.NewReader("oversized entry\n"), "--git-dir", ".", "-c", "user.name=Page Test", "-c", "user.email=page@example.invalid", "commit-tree", strings.TrimSpace(string(tree.Stdout)), "-p", oid)
	noErr(t, err)
	apiRunGit(t, remote, "update-ref", "refs/heads/main", strings.TrimSpace(string(commit.Stdout)))
	wroteRefs(app, "syntax")
	for _, viewer := range []struct {
		client *http.Client
		home   string
	}{{&http.Client{}, server.URL + "/repositories/syntax"}, {shared, home}} {
		body, status := dashboardGET(t, viewer.client, viewer.home+"/code")
		if status != http.StatusServiceUnavailable || strings.Contains(body, "This folder is empty.") || strings.Contains(body, "Showing ") {
			t.Errorf("oversized listing: status=%d", status)
		}
	}
	// A revoked authenticated link also keeps its established not-found response.
	revoked := adminAPIRequest(t, http.MethodPost, server.URL+"/api/v1/repositories/syntax/share-links/"+created.ShareLink.ID+"/revoke", nil, "admin-password")
	revoked.Body.Close()
	_, status := dashboardGET(t, shared, home+"/code?revision=bad")
	if status != http.StatusNotFound {
		t.Errorf("revoked link revealed syntax: status=%d", status)
	}
	// Invalid or unopened links reveal no page-address errors.
	for _, address := range []string{home + "/code?revision=bad", server.URL + "/share/00000000000000000000000000000000/code?revision=bad"} {
		_, status := dashboardGET(t, &http.Client{}, address)
		if status != http.StatusNotFound {
			t.Errorf("unopened link revealed syntax: status=%d", status)
		}
	}
}
