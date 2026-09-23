package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
)

func TestDashboardPreservesCollidingBranchAndTagIdentity(t *testing.T) {
	app := newConfiguredApp(t)
	if _, err := app.Repositories.Create(context.Background(), "collision", ""); err != nil {
		t.Fatal(err)
	}
	remote, _ := app.Repositories.Path("collision")
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Collision Author")
	apiRunGit(t, work, "config", "user.email", "collision@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "identity.txt"), []byte("tag identity\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "tag identity")
	apiRunGit(t, work, "tag", "same")
	apiRunGit(t, work, "remote", "add", "origin", remote)
	apiRunGit(t, work, "push", "origin", "refs/tags/same")
	noErr(t, os.WriteFile(filepath.Join(work, "identity.txt"), []byte("branch identity\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "branch identity")
	apiRunGit(t, work, "push", "origin", "HEAD:refs/heads/same")

	server := serve(t, app.Handler())
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
	app := newConfiguredApp(t)
	if _, err := app.Repositories.Create(context.Background(), "real-project", "A real repository"); err != nil {
		t.Fatal(err)
	}
	remote, _ := app.Repositories.Path("real-project")
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Dashboard Author")
	apiRunGit(t, work, "config", "user.email", "dashboard@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "unsafe.html"), []byte("<script>alert('escaped')</script>\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	commit := exec.Command("git", "commit", "-m", "Render actual repository data")
	commit.Dir = work
	commit.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2024-04-05T23:30:00-07:00", "GIT_COMMITTER_DATE=2024-04-06T08:00:00Z")
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, output)
	}
	oid := apiGitOutput(t, work, "rev-parse", "HEAD")
	apiRunGit(t, work, "remote", "add", "origin", remote)
	apiRunGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	server := serve(t, app.Handler())
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

	noErr(t, os.WriteFile(filepath.Join(work, "release-only.txt"), []byte("recover this branch\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "release-only work")
	releaseOID := apiGitOutput(t, work, "rev-parse", "HEAD")
	apiRunGit(t, work, "push", "origin", "HEAD:refs/heads/release")
	apiRunGit(t, work, "push", "origin", ":refs/heads/release")
	recovered := "recovered-" + shortOID(releaseOID)
	body, status = dashboardGET(t, client, server.URL+"/repositories/real-project")
	retainedRestoreLink := `/repositories/real-project/restore?source=` + releaseOID + `&amp;target=` + recovered
	if status != http.StatusOK || !strings.Contains(body, retainedRestoreLink) {
		t.Fatalf("retained history did not expose a safe restore target: status=%d link=%s", status, retainedRestoreLink)
	}
	restoreBody, restoreStatus := dashboardGET(t, client, server.URL+"/repositories/real-project/restore?source="+releaseOID+"&target="+recovered)
	if restoreStatus != http.StatusOK || !strings.Contains(restoreBody, recovered) {
		t.Fatalf("retained restore did not keep its missing target default: status=%d", restoreStatus)
	}
	preview, err := app.Repositories.PreviewRestore(context.Background(), "real-project", repository.RestoreRequest{
		Source: releaseOID, Target: recovered, Mode: repository.RestoreAll,
	})
	if err != nil || !preview.CreatesBranch || preview.ExpectedHead != strings.Repeat("0", len(oid)) {
		t.Fatalf("retained restore target was not a missing branch: preview=%+v err=%v", preview, err)
	}

	apiRunGit(t, work, "checkout", "--orphan", "replacement")
	apiRunGit(t, work, "rm", "-rf", ".")
	noErr(t, os.WriteFile(filepath.Join(work, "replacement.txt"), []byte("replacement\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "replacement")
	apiRunGit(t, work, "push", "--force", "origin", "HEAD:refs/heads/main")
	body, status = dashboardGET(t, client, server.URL+"/repositories/real-project/commits/"+oid)
	if status != http.StatusOK || !strings.Contains(body, "Showing a specific revision, not a branch") || strings.Contains(body, "ref=refs%2Fheads%2Fmain") {
		t.Fatalf("retained activity commit was not shown detached from current main: status=%d", status)
	}

	apiRunGit(t, work, "push", "origin", ":refs/heads/main")
	body, status = dashboardGET(t, client, server.URL+"/repositories/real-project")
	if status != http.StatusOK || !strings.Contains(body, shortOID(oid)) || !strings.Contains(body, "default branch no longer exists") {
		t.Fatalf("deleted default branch page status=%d did not show retained history and missing default", status)
	}
	body, status = dashboardGET(t, client, server.URL+"/repositories/real-project/commits/"+oid)
	if status != http.StatusOK || !strings.Contains(body, "Render actual repository data") || !strings.Contains(body, "&lt;script&gt;alert(&#39;escaped&#39;)&lt;/script&gt;") {
		t.Fatalf("retained commit was not browsable after deleting the last branch: status=%d", status)
	}
	activity, err := app.Repositories.Activity(context.Background(), "real-project", 100)
	noErr(t, err)
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

// addActivityRepository creates a repository with the requested number of
// commits on main and returns its bare path and a working clone.
func addActivityRepository(t *testing.T, app *App, name string, commits int) (remote, work string) {
	t.Helper()
	if _, err := app.Repositories.Create(context.Background(), name, ""); err != nil {
		t.Fatal(err)
	}
	remote, _ = app.Repositories.Path(name)
	work = filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Activity Author")
	apiRunGit(t, work, "config", "user.email", "activity@example.invalid")
	for index := 0; index < commits; index++ {
		noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte(strings.Repeat("x", index+1)+"\n"), 0o600))
		apiRunGit(t, work, "add", ".")
		apiRunGit(t, work, "commit", "-m", "commit")
	}
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main")
	return remote, work
}

// newActivityFixture creates an open-mode app with one repository that has the
// requested number of commits on main.
func newActivityFixture(t *testing.T, name string, commits int) (app *App, remote, work string) {
	t.Helper()
	app = newConfiguredApp(t)
	remote, work = addActivityRepository(t, app, name, commits)
	return app, remote, work
}

// traceGitCommands replaces the app's Git runner with a wrapper that records
// every invocation and returns the trace path.
func traceGitCommands(t *testing.T, app *App) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	tracePath := filepath.Join(t.TempDir(), "git-commands")
	wrapperPath := filepath.Join(t.TempDir(), "git-wrapper")
	wrapper := "#!/bin/sh\nprintf '%s\\0' \"$@\" >> " + serverShellQuote(tracePath) + "\nprintf '\\n' >> " + serverShellQuote(tracePath) + "\nexec " + serverShellQuote(gitPath) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapperPath, []byte(wrapper), 0o700))
	traced, err := gitexec.New(wrapperPath, filepath.Join(t.TempDir(), "runtime"))
	noErr(t, err)
	app.Repositories.Git = traced
	noErr(t, os.WriteFile(tracePath, nil, 0o600))
	return tracePath
}

// traceActivityLogs replaces the app's Git runner with a wrapper that records
// one tab-separated line per invocation as "<effective-git-dir>\t<subcommand>\t<kind>".
// A relative Git directory is resolved from the process working directory.
// History walks are recorded as "current" or "retained" from the revision
// input on stdin, so a test can count real walks per repository instead of
// inferring them from another command's absence.
func traceActivityLogs(t *testing.T, app *App) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "git-commands")
	wrapperPath := filepath.Join(dir, "git-wrapper")
	wrapper := "#!/bin/sh\n" +
		"dir=\"\"\n" +
		"skip=0\n" +
		"sub=\"\"\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$skip\" = \"1\" ]; then skip=0; continue; fi\n" +
		"  case \"$a\" in\n" +
		"    --git-dir) skip=1 ;;\n" +
		"    -*) ;;\n" +
		"    *) sub=\"$a\"; break ;;\n" +
		"  esac\n" +
		"done\n" +
		"prev=\"\"\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--git-dir\" ]; then dir=\"$a\"; fi\n" +
		"  prev=\"$a\"\n" +
		"done\n" +
		"if [ \"$dir\" = \".\" ]; then dir=$(pwd -P) || exit 1; fi\n" +
		"activity=0\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"--source\" ]; then activity=1; fi\n" +
		"done\n" +
		"if [ \"$activity\" = \"1\" ]; then\n" +
		"  input=$(mktemp)\n" +
		"  cat > \"$input\"\n" +
		"  kind=current\n" +
		"  if grep -q '^\\^' \"$input\"; then kind=retained; fi\n" +
		"  printf '%s\\t%s\\t%s\\n' \"$dir\" \"$sub\" \"$kind\" >> " + serverShellQuote(tracePath) + "\n" +
		"  exec " + serverShellQuote(gitPath) + " \"$@\" < \"$input\"\n" +
		"fi\n" +
		"printf '%s\\t%s\\t-\\n' \"$dir\" \"$sub\" >> " + serverShellQuote(tracePath) + "\n" +
		"exec " + serverShellQuote(gitPath) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapperPath, []byte(wrapper), 0o700))
	traced, err := gitexec.New(wrapperPath, filepath.Join(dir, "runtime"))
	noErr(t, err)
	app.Repositories.Git = traced
	noErr(t, os.WriteFile(tracePath, nil, 0o600))
	return tracePath
}

// activityLogCounts maps a repository name to its history-walk kinds.
func activityLogCounts(t *testing.T, tracePath string) map[string]map[string]int {
	t.Helper()
	trace, err := os.ReadFile(tracePath)
	noErr(t, err)
	counts := map[string]map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(trace)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[2] == "-" {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(fields[0]), ".git")
		if counts[name] == nil {
			counts[name] = map[string]int{}
		}
		counts[name][fields[2]]++
	}
	return counts
}

func TestOverviewAndActivityShareOneBoundedObservation(t *testing.T) {
	app, _, _ := newActivityFixture(t, "bounded", 3)
	app.ActivityLimit = 2
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	for _, target := range []string{"/", "/activity"} {
		body, status := dashboardGET(t, client, server.URL+target)
		if status != http.StatusOK || !strings.Contains(body, "This count is incomplete") {
			t.Fatalf("GET %s status=%d incomplete=%v", target, status, strings.Contains(body, "This count is incomplete"))
		}
	}
}

func TestOverviewUsesOneActivityScanPerRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	app, _, _ := newActivityFixture(t, "scanned", 3)
	tracePath := traceActivityLogs(t, app)
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if _, status := dashboardGET(t, client, server.URL+"/"); status != http.StatusOK {
		t.Fatalf("overview status=%d", status)
	}
	// One current walk per repository. A separate retained walk is allowed
	// when retained history exists, and this fixture has none.
	counts := activityLogCounts(t, tracePath)
	if counts["scanned"]["current"] != 1 || counts["scanned"]["retained"] != 0 {
		t.Fatalf("overview history walks=%v, want one current walk", counts)
	}
	// The shared observation replaces the separate combined walk that the
	// graph used to run with its own budget.
	trace, err := os.ReadFile(tracePath)
	noErr(t, err)
	if count := bytes.Count(trace, []byte("\trev-list\t")); count != 0 {
		t.Fatalf("overview used %d separate history walks, want 0", count)
	}
}

// TestOverviewActivityBudgetIsPageWideAcrossRepositories spends the whole
// page budget on the first repository and proves the second is never scanned,
// so the rendered total stops at the budget instead of the full history.
func TestOverviewActivityBudgetIsPageWideAcrossRepositories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	app := newConfiguredApp(t)
	app.ActivityLimit = 2
	addActivityRepository(t, app, "alpha", 3)
	addActivityRepository(t, app, "beta", 3)
	tracePath := traceActivityLogs(t, app)
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body, status := dashboardGET(t, client, server.URL+"/")
	if status != http.StatusOK {
		t.Fatalf("overview status=%d", status)
	}
	counts := activityLogCounts(t, tracePath)
	if counts["alpha"]["current"] != 1 || counts["alpha"]["retained"] != 0 {
		t.Fatalf("first repository walks=%v, want one current walk", counts["alpha"])
	}
	if len(counts["beta"]) != 0 {
		t.Fatalf("second repository was scanned after the budget was spent: %v", counts["beta"])
	}
	if !strings.Contains(body, "This count is incomplete") {
		t.Fatal("overview did not report the truncated observation")
	}
	if !strings.Contains(body, "2 commits in ") || strings.Contains(body, "6 commits in ") {
		t.Fatalf("overview total did not stop at the page budget")
	}
}

func TestActivityObservationFailureMarksGraphUnavailable(t *testing.T) {
	app, remote, _ := newActivityFixture(t, "unreadable-activity", 1)
	// A commit whose parent is missing makes the branch readable to
	// for-each-ref but not to the history walk.
	treeOID := apiGitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main^{tree}")
	commitObject := "tree " + treeOID + "\nparent " + strings.Repeat("1", 40) + "\nauthor Test <test@example.invalid> 1704067200 +0000\ncommitter Test <test@example.invalid> 1704067200 +0000\n\nbroken parent\n"
	command := exec.Command("git", "--git-dir", remote, "hash-object", "-t", "commit", "-w", "--stdin")
	command.Stdin = strings.NewReader(commitObject)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("create broken commit object: %v\n%s", err, output)
	}
	apiRunGit(t, "", "--git-dir", remote, "update-ref", "refs/heads/broken", strings.TrimSpace(string(output)))

	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	for _, target := range []string{"/", "/activity"} {
		body, status := dashboardGET(t, client, server.URL+target)
		if status != http.StatusOK || !strings.Contains(body, "Activity is not available") {
			t.Fatalf("GET %s status=%d unavailable=%v", target, status, strings.Contains(body, "Activity is not available"))
		}
	}
}

func TestRepositoryPageBatchesRefTipMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	app, remote, work := newActivityFixture(t, "batched", 1)
	for _, branch := range []string{"one", "two", "three"} {
		apiRunGit(t, work, "branch", branch)
	}
	apiRunGit(t, work, "tag", "lightweight")
	apiRunGit(t, work, "tag", "-a", "annotated", "-m", "annotated")
	apiRunGit(t, work, "push", remote, "refs/heads/main", "refs/heads/one", "refs/heads/two", "refs/heads/three", "refs/tags/lightweight", "refs/tags/annotated")
	tracePath := traceGitCommands(t, app)

	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body, status := dashboardGET(t, client, server.URL+"/repositories/batched")
	if status != http.StatusOK {
		t.Fatalf("repository page status=%d", status)
	}
	for _, name := range []string{"one", "two", "three", "lightweight", "annotated"} {
		if !strings.Contains(body, name) {
			t.Fatalf("repository page lost ref %q", name)
		}
	}
	trace, err := os.ReadFile(tracePath)
	noErr(t, err)
	// One batched metadata read for branches and one for tags replaces two
	// Git processes per ref.
	if count := bytes.Count(trace, []byte("\x00--no-walk\x00")); count != 2 {
		t.Fatalf("repository page used %d batched tip reads, want 2", count)
	}
}

// TestRepositoryPageKeepsRefRowsWhenOneMetadataBatchFails breaks the tag
// metadata batch with a tag object whose target is missing. The page must
// still render both ref groups, keep the healthy branch tip, and drop only
// the failed group's tips.
func TestRepositoryPageKeepsRefRowsWhenOneMetadataBatchFails(t *testing.T) {
	app, remote, work := newActivityFixture(t, "degraded", 1)
	apiRunGit(t, work, "checkout", "-b", "feature")
	noErr(t, os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "feature tip")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/feature")
	apiRunGit(t, work, "tag", "-a", "good", "-m", "good")
	apiRunGit(t, work, "push", remote, "refs/tags/good")
	// A tag object whose target is missing makes the batch peel fail while
	// for-each-ref still lists the ref.
	tagObject := "object " + strings.Repeat("1", 40) + "\ntype commit\ntag broken\ntagger Test <test@example.invalid> 1704067200 +0000\n\nbroken\n"
	command := exec.Command("git", "--git-dir", remote, "hash-object", "-t", "tag", "-w", "--stdin")
	command.Stdin = strings.NewReader(tagObject)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("create broken tag object: %v\n%s", err, output)
	}
	apiRunGit(t, "", "--git-dir", remote, "update-ref", "refs/tags/broken", strings.TrimSpace(string(output)))

	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body, status := dashboardGET(t, client, server.URL+"/repositories/degraded")
	if status != http.StatusOK {
		t.Fatalf("repository page status=%d", status)
	}
	branches := pageSection(t, body, "Branches")
	tags := pageSection(t, body, "Tags")
	if !strings.Contains(branches, ">feature<") || !strings.Contains(branches, "feature tip") || !strings.Contains(branches, "refline__act") {
		t.Fatalf("healthy branch group lost its row or tip:\n%s", branches)
	}
	if !strings.Contains(tags, ">good<") || !strings.Contains(tags, ">broken<") {
		t.Fatalf("failed tag group lost a ref name:\n%s", tags)
	}
	if strings.Contains(tags, "feature tip") || strings.Contains(tags, "refline__act") {
		t.Fatalf("failed tag group kept a tip:\n%s", tags)
	}
}

// pageSection returns the rendered section that starts at the given section
// heading. It matches the h2, because the overview's summary strip names the
// same groups as labels above their counts.
func pageSection(t *testing.T, body, heading string) string {
	t.Helper()
	start := strings.Index(body, ">"+heading+"</span></h2>")
	if start < 0 {
		t.Fatalf("page has no %s section", heading)
	}
	end := strings.Index(body[start:], "</section>")
	if end < 0 {
		t.Fatalf("page has no end for the %s section", heading)
	}
	return body[start : start+end]
}

func dashboardGET(t *testing.T, client *http.Client, target string) (string, int) {
	t.Helper()
	result := browserGET(t, client, target)
	return result.body, result.status
}
func gitCombined(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	return string(output), err
}
