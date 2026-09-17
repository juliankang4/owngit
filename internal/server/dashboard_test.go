package server

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/auth"
)

func TestDashboardPreservesCollidingBranchAndTagIdentity(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(repositoryRoot)
	adminHash, _ := auth.HashPassword("admin-password")
	if err := store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, true); err != nil {
		t.Fatal(err)
	}
	app.Repositories.SetRoot(canonical)
	if _, err := app.Repositories.Create(context.Background(), "collision", ""); err != nil {
		t.Fatal(err)
	}
	remote, _ := app.Repositories.Path("collision")
	work := filepath.Join(t.TempDir(), "work")
	runDashboardGit(t, "", "init", "--initial-branch=main", work)
	runDashboardGit(t, work, "config", "user.name", "Collision Author")
	runDashboardGit(t, work, "config", "user.email", "collision@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "identity.txt"), []byte("tag identity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDashboardGit(t, work, "add", ".")
	runDashboardGit(t, work, "commit", "-m", "tag identity")
	runDashboardGit(t, work, "tag", "same")
	runDashboardGit(t, work, "remote", "add", "origin", remote)
	runDashboardGit(t, work, "push", "origin", "refs/tags/same")
	if err := os.WriteFile(filepath.Join(work, "identity.txt"), []byte("branch identity\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDashboardGit(t, work, "add", ".")
	runDashboardGit(t, work, "commit", "-m", "branch identity")
	runDashboardGit(t, work, "push", "origin", "HEAD:refs/heads/same")

	server := httptest.NewServer(app.Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	branchBody, branchStatus := dashboardGET(t, client, server.URL+"/repositories/collision/code?ref=refs%2Fheads%2Fsame&path=identity.txt")
	if branchStatus != http.StatusOK || !strings.Contains(branchBody, "branch identity") || strings.Contains(branchBody, "tag identity") || !strings.Contains(branchBody, "ref=refs%2Fheads%2Fsame") {
		t.Fatalf("branch selection lost its canonical identity: status=%d", branchStatus)
	}
	tagBody, tagStatus := dashboardGET(t, client, server.URL+"/repositories/collision/code?ref=refs%2Ftags%2Fsame&path=identity.txt")
	if tagStatus != http.StatusOK || !strings.Contains(tagBody, "tag identity") || strings.Contains(tagBody, "branch identity") || !strings.Contains(tagBody, "ref=refs%2Ftags%2Fsame") {
		t.Fatalf("tag selection lost its canonical identity: status=%d", tagStatus)
	}
	activityBody, activityStatus := dashboardGET(t, client, server.URL+"/activity")
	if activityStatus != http.StatusOK || !strings.Contains(activityBody, "ref=refs%2Fheads%2Fsame") {
		t.Fatalf("activity link did not preserve canonical branch identity: status=%d", activityStatus)
	}
}

func TestDashboardRendersRealEscapedGitDataAndRetainedHistory(t *testing.T) {
	app, store, repositoryRoot := newTestApp(t)
	if err := os.MkdirAll(repositoryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	adminHash, _ := auth.HashPassword("admin-password")
	if err := store.CompleteSetup(context.Background(), canonical, "open", "", adminHash, true); err != nil {
		t.Fatal(err)
	}
	app.Repositories.SetRoot(canonical)
	if _, err := app.Repositories.Create(context.Background(), "real-project", "A real repository"); err != nil {
		t.Fatal(err)
	}
	remote, _ := app.Repositories.Path("real-project")
	work := filepath.Join(t.TempDir(), "work")
	runDashboardGit(t, "", "init", "--initial-branch=main", work)
	runDashboardGit(t, work, "config", "user.name", "Dashboard Author")
	runDashboardGit(t, work, "config", "user.email", "dashboard@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "unsafe.html"), []byte("<script>alert('escaped')</script>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDashboardGit(t, work, "add", ".")
	commit := exec.Command("git", "commit", "-m", "Render actual repository data")
	commit.Dir = work
	commit.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2024-04-05T23:30:00-07:00", "GIT_COMMITTER_DATE=2024-04-06T08:00:00Z")
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, output)
	}
	oid := dashboardGitOutput(t, work, "rev-parse", "HEAD")
	runDashboardGit(t, work, "remote", "add", "origin", remote)
	runDashboardGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	server := httptest.NewServer(app.Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	for target, expected := range map[string]string{
		"/": "Render actual repository data",
		"/repositories/real-project/code?ref=main&path=unsafe.html":                "&lt;script&gt;alert(&#39;escaped&#39;)&lt;/script&gt;",
		"/repositories/real-project/commits/" + oid + "?ref=main&path=unsafe.html": "&lt;script&gt;alert(&#39;escaped&#39;)&lt;/script&gt;",
		"/activity?year=2024": "Render actual repository data",
	} {
		body, status := dashboardGET(t, client, server.URL+target)
		if status != http.StatusOK || !strings.Contains(body, expected) {
			t.Errorf("GET %s status=%d missing %q", target, status, expected)
		}
		if strings.Contains(body, "<script>alert('escaped')</script>") {
			t.Errorf("GET %s executed repository text as markup", target)
		}
	}
	body, status := dashboardGET(t, client, server.URL+"/repositories/real-project?ref=refs%2Fheads%2Fmissing")
	if status != http.StatusOK || !strings.Contains(body, "That branch or tag does not exist") || strings.Contains(body, "default branch no longer exists") {
		t.Fatalf("explicit missing ref used the wrong notice: status=%d", status)
	}

	runDashboardGit(t, work, "checkout", "--orphan", "replacement")
	runDashboardGit(t, work, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(work, "replacement.txt"), []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runDashboardGit(t, work, "add", ".")
	runDashboardGit(t, work, "commit", "-m", "replacement")
	runDashboardGit(t, work, "push", "--force", "origin", "HEAD:refs/heads/main")
	body, status = dashboardGET(t, client, server.URL+"/repositories/real-project/commits/"+oid)
	if status != http.StatusOK || !strings.Contains(body, "Showing a specific revision, not a branch") || strings.Contains(body, "ref=refs%2Fheads%2Fmain") {
		t.Fatalf("retained activity commit was not shown detached from current main: status=%d", status)
	}

	runDashboardGit(t, work, "push", "origin", ":refs/heads/main")
	body, status = dashboardGET(t, client, server.URL+"/repositories/real-project")
	if status != http.StatusOK || !strings.Contains(body, shortOID(oid)) || !strings.Contains(body, "default branch no longer exists") {
		t.Fatalf("deleted default branch page status=%d did not show retained history and missing default", status)
	}
	body, status = dashboardGET(t, client, server.URL+"/repositories/real-project/commits/"+oid)
	if status != http.StatusOK || !strings.Contains(body, "Render actual repository data") || !strings.Contains(body, "&lt;script&gt;alert(&#39;escaped&#39;)&lt;/script&gt;") {
		t.Fatalf("retained commit was not browsable after deleting the last branch: status=%d", status)
	}
	activity, err := app.Repositories.Activity(context.Background(), "real-project", 100)
	if err != nil {
		t.Fatal(err)
	}
	foundRecordedDay := false
	for _, day := range activity.Days {
		if day.Day == "2024-04-05" && day.Count == 1 {
			foundRecordedDay = true
		}
	}
	if !foundRecordedDay {
		t.Fatalf("activity did not use the author's recorded day: %+v", activity.Days)
	}
}

func dashboardGET(t *testing.T, client *http.Client, target string) (string, int) {
	t.Helper()
	response, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(content), response.StatusCode
}

func runDashboardGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	if output, err := dashboardGitCombined(directory, arguments...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func dashboardGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	output, err := dashboardGitCombined(directory, arguments...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(output)
}

func dashboardGitCombined(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	return string(output), err
}
