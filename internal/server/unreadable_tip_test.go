package server

import (
	"context"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/webui"
)

// pushCommit creates repository id with one commit on main and other,
// authored on 2024-03-04, and returns its Git directory and a work tree.
func pushCommit(t *testing.T, app *App, id, subject string) (string, string) {
	t.Helper()
	if _, err := app.Repositories.Create(context.Background(), id, ""); err != nil {
		t.Fatal(err)
	}
	remote, _ := app.Repositories.Path(id)
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Tip Author")
	apiRunGit(t, work, "config", "user.email", "tip@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("readable file\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "--date=2024-03-04T05:06:07+09:00", "-m", subject)
	apiRunGit(t, work, "remote", "add", "origin", remote)
	apiRunGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/other")
	return remote, work
}

// A pushed default branch tip without an author line cannot be read. Every
// page names that commit and shows what else it can: the dashboard keeps the
// other repositories' activity and names the one left out, the overview and
// the history show the readable parent, and the commit's own page says which
// commit could not be read and why. The log names the repository and the
// commit.
func TestUnreadableDefaultTipKeepsOtherPagesWorking(t *testing.T) {
	app := newConfiguredApp(t)
	pushCommit(t, app, "good", "good repository commit")
	remote, work := pushCommit(t, app, "tip", "readable parent")
	parent := apiGitOutput(t, work, "rev-parse", "HEAD")
	tree := apiGitOutput(t, work, "rev-parse", "HEAD^{tree}")
	command := exec.Command("git", "--git-dir", remote, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
	command.Stdin = strings.NewReader("tree " + tree + "\nparent " + parent + "\ncommitter C <c@example.invalid> 1700000000 +0000\n\nno author\n")
	output, err := command.CombinedOutput()
	noErr(t, err)
	tip := strings.TrimSpace(string(output))
	apiRunGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/main", tip)

	var serverLog lockedLog
	previousLog, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&serverLog)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousLog)
		log.SetFlags(previousFlags)
	})
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	en := func(code webui.MessageCode) string { return webui.Text(webui.LangEN, code) }

	dashboard, status := dashboardGET(t, client, server.URL+"/?year=2024")
	if status != http.StatusOK || !strings.Contains(dashboard, en(webui.MsgRepoHeadUnreadable)) {
		t.Fatalf("dashboard status=%d does not say the latest commit could not be read", status)
	}
	if strings.Contains(dashboard, en(webui.MsgActivityScanFail)) || !strings.Contains(dashboard, "1 commit in 2024") ||
		!strings.Contains(dashboard, en(webui.MsgActivityLeftOut)+`</span> <span dir="auto">tip</span>`) {
		t.Fatalf("dashboard activity does not count the readable repository and name the unreadable one:\n%s", dashboard)
	}

	notice := en(webui.MsgCommitUnreadable)
	// The overview names the tip once, although both its recent commits
	// and its activity graph failed on it.
	for _, path := range []string{"/repositories/tip", "/repositories/tip/commits", "/repositories/tip/commits/" + tip} {
		body, status := dashboardGET(t, client, server.URL+path)
		if status != http.StatusOK || strings.Contains(body, en(webui.MsgRepoUnreadable)) || !strings.Contains(body, notice) || strings.Count(body, ">"+shortOID(tip)+"</a>") != 1 {
			t.Fatalf("%s: status=%d does not name the unreadable commit", path, status)
		}
		if path != "/repositories/tip/commits/"+tip && !strings.Contains(body, "readable parent") {
			t.Fatalf("%s does not show the readable parent", path)
		}
	}
	for _, path := range []string{"/repositories/tip?ref=other", "/repositories/tip/code?ref=other&path=file.txt", "/repositories/tip/commits?ref=other", "/repositories/tip/pull-requests"} {
		body, status := dashboardGET(t, client, server.URL+path)
		if status != http.StatusOK || strings.Contains(body, en(webui.MsgRepoUnreadable)) {
			t.Fatalf("%s: status=%d", path, status)
		}
	}

	logged := serverLog.String()
	for _, want := range []string{`activity of repository "tip" could not be counted: "commit ` + tip, `repository \"tip\": commit ` + tip} {
		if !strings.Contains(logged, want) {
			t.Fatalf("the log does not contain %q:\n%s", want, logged)
		}
	}
}
