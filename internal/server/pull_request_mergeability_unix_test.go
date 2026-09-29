//go:build !windows

package server

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"owngit/internal/pullrequest"
)

// Opening the pull request list or page never works out a merge; only
// Check mergeability does.
func TestPullRequestPagesDoNotCalculateMerges(t *testing.T) {
	fixture := newAPIFixture(t, false)
	divergeWithConflict(t, fixture)
	created, err := fixture.app.PullRequests.Create(context.Background(), pullrequest.CreateInput{Repository: "project", Title: "Feature", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	realGit, err := exec.LookPath("git")
	noErr(t, err)
	marker := filepath.Join(t.TempDir(), "merge-tree-ran")
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\ncase \" $* \" in *\" merge-tree \"*) : > '" + marker + "';; esac\nexec '" + realGit + "' \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	fixture.app.Repositories.Git.GitPath = wrapper

	server, client, jar := openBrowser(t, fixture)
	page := server.URL + pullRequestURL("project", created.Number)
	for _, target := range []string{server.URL + "/repositories/project/pull-requests", page, server.URL + "/api/v1/repositories/project/pull-requests"} {
		if result := browserGET(t, client, target); result.status != http.StatusOK {
			t.Fatalf("GET %s status=%d", target, result.status)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("opening pull requests calculated a merge: %v", err)
	}
	show, err := fixture.app.PullRequests.Show(context.Background(), "project", created.Number)
	noErr(t, err)
	check := browserForm(t, client, page+"/mergeability", url.Values{
		"csrf": {cookieValue(t, jar, server.URL, generalCookie)}, "source_oid": {show.Source.OID}, "target_oid": {show.Target.OID},
	}, server.URL)
	if _, err := os.Stat(marker); check.status != http.StatusOK || err != nil {
		t.Fatalf("Check mergeability status=%d marker=%v", check.status, err)
	}
}
