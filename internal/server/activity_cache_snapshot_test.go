package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/pullrequest"
	"owngit/internal/webui"
)

// TestWarmDashboardStartsNoGitProcess proves that a dashboard request for
// repositories whose refs have not changed since the previous request starts
// no Git process, and that a push through Smart HTTP and a pull request merge
// in the browser are both on the next dashboard.
func TestWarmDashboardStartsNoGitProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	fixture := newAPIFixture(t, false)
	for _, name := range []string{"second", "third"} {
		addActivityRepository(t, fixture.app, name, 2)
	}
	tracePath := traceGitCommands(t, fixture.app)
	server, client, jar := openBrowser(t, fixture)
	processes := func() int {
		t.Helper()
		trace, err := os.ReadFile(tracePath)
		noErr(t, err)
		return strings.Count(string(trace), "\n")
	}

	settledGET(t, client, server.URL+"/")
	before := processes()
	body := settledGET(t, client, server.URL+"/")
	if started := processes() - before; started != 0 {
		t.Fatalf("a warm dashboard started %d Git processes, want 0", started)
	}
	if !strings.Contains(body, fixture.targetOID) {
		t.Fatal("the warm dashboard does not show the default branch tip")
	}

	// Push through Smart HTTP.
	noErr(t, os.WriteFile(filepath.Join(fixture.work, "pushed.txt"), []byte("pushed\n"), 0o600))
	apiRunGit(t, fixture.work, "checkout", "main")
	apiRunGit(t, fixture.work, "add", ".")
	apiRunGit(t, fixture.work, "commit", "-m", "pushed through Smart HTTP")
	apiRunGit(t, fixture.work, "push", server.URL+"/git/project.git", "HEAD:refs/heads/main")
	pushed := apiGitOutput(t, fixture.work, "rev-parse", "HEAD")
	body = settledGET(t, client, server.URL+"/")
	if !strings.Contains(body, pushed) || !strings.Contains(body, "pushed through Smart HTTP") {
		t.Fatal("the dashboard after a Smart HTTP push does not show the pushed tip")
	}
	before = processes()
	settledGET(t, client, server.URL+"/")
	if started := processes() - before; started != 0 {
		t.Fatalf("the dashboard after the push settled still started %d Git processes", started)
	}

	// Merge a pull request in the browser.
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "project", Title: "Snapshot merge", SourceBranch: "feature", TargetBranch: "main",
		ReviewChoice: webui.ReviewChoiceSkip,
	})
	noErr(t, err)
	merge := browserForm(t, client, server.URL+pullRequestURL("project", created.Number)+"/merge", url.Values{
		"csrf":       {cookieValue(t, jar, server.URL, generalCookie)},
		"source_oid": {fixture.sourceOID},
		"target_oid": {pushed},
	}, server.URL)
	if merge.status != http.StatusSeeOther {
		t.Fatalf("merge status=%d", merge.status)
	}
	merged := apiGitOutput(t, "", "--git-dir", fixture.remote, "rev-parse", "refs/heads/main")
	if merged == pushed {
		t.Fatal("the merge did not move main")
	}
	snapshot, err := fixture.app.Repositories.RefSnapshot(context.Background(), "project")
	noErr(t, err)
	if snapshot.Summary.DefaultOID != merged || snapshot.Head.OID != merged {
		t.Fatalf("snapshot after merge=%+v, want main at %s", snapshot.Summary, merged)
	}
	if body := settledGET(t, client, server.URL+"/"); !strings.Contains(body, merged) {
		t.Fatal("the dashboard after a merge does not show the merged tip")
	}
}
