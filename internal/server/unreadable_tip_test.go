package server

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/webui"
)

// A pushed default branch tip without an author line cannot be read. The
// dashboard row says the latest commit could not be read, the repository's
// other pages keep working, and the tip's own history reports the failure.
func TestUnreadableDefaultTipKeepsOtherPagesWorking(t *testing.T) {
	app := newConfiguredApp(t)
	if _, err := app.Repositories.Create(context.Background(), "tip", ""); err != nil {
		t.Fatal(err)
	}
	remote, _ := app.Repositories.Path("tip")
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Tip Author")
	apiRunGit(t, work, "config", "user.email", "tip@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("readable file\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "readable other branch")
	apiRunGit(t, work, "remote", "add", "origin", remote)
	apiRunGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/other")
	tree := apiGitOutput(t, work, "rev-parse", "HEAD^{tree}")
	command := exec.Command("git", "--git-dir", remote, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
	command.Stdin = strings.NewReader("tree " + tree + "\ncommitter C <c@example.invalid> 1700000000 +0000\n\nno author\n")
	output, err := command.CombinedOutput()
	noErr(t, err)
	apiRunGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/main", strings.TrimSpace(string(output)))

	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	dashboard, status := dashboardGET(t, client, server.URL+"/")
	if status != http.StatusOK || !strings.Contains(dashboard, webui.Text(webui.LangEN, webui.MsgRepoHeadUnreadable)) {
		t.Fatalf("dashboard status=%d does not say the latest commit could not be read", status)
	}
	for _, path := range []string{"/repositories/tip?ref=other", "/repositories/tip/code?ref=other&path=file.txt", "/repositories/tip/commits?ref=other", "/repositories/tip/pull-requests"} {
		body, status := dashboardGET(t, client, server.URL+path)
		if status != http.StatusOK || strings.Contains(body, webui.Text(webui.LangEN, webui.MsgRepoUnreadable)) {
			t.Fatalf("%s: status=%d", path, status)
		}
	}
	if _, status := dashboardGET(t, client, server.URL+"/repositories/tip/commits"); status == http.StatusOK {
		t.Fatal("the unreadable tip's history answered 200")
	}
}
