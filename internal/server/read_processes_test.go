package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/testfixture"
	"owngit/internal/webui"
)

// wroteRefs takes and releases the repository write lock, as every OwnGit
// ref writer does. A fixture that pushes to the storage folder directly
// calls it, since OwnGit sees such refs after its next write.
func wroteRefs(app *App, id string) {
	lock := app.Repositories.Locks.For(id)
	lock.Lock()
	lock.Unlock()
}

// countGit replaces the app's Git with a wrapper that records one line per
// Git process it starts, and returns the lines recorded since the last call.
func countGit(t *testing.T, app *App) func() []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a POSIX-shell fixture")
	}
	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace")
	wrapper := filepath.Join(dir, "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" | tr '\\n' ' ' >> " + serverShellQuote(tracePath) + "\necho >> " + serverShellQuote(tracePath) + "\nexec " + serverShellQuote(gitPath) + " \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	runner, err := gitexec.New(wrapper, filepath.Join(dir, "runtime"))
	noErr(t, err)
	app.Repositories.Git = runner
	noErr(t, os.WriteFile(tracePath, nil, 0o600))
	seen := 0
	return func() []string {
		t.Helper()
		trace, err := os.ReadFile(tracePath)
		noErr(t, err)
		lines := strings.Split(strings.TrimSuffix(string(trace), "\n"), "\n")
		if len(trace) == 0 {
			lines = nil
		}
		fresh := lines[seen:]
		seen = len(lines)
		return fresh
	}
}

// commitTo commits files in work and force-pushes the commit to branch. The
// push bypasses OwnGit, so it also records an OwnGit write.
func commitTo(t *testing.T, app *App, id, work, branch string, files map[string]string, message string, when time.Time) string {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(work, filepath.FromSlash(name))
		noErr(t, os.MkdirAll(filepath.Dir(full), 0o700))
		noErr(t, os.WriteFile(full, []byte(content), 0o600))
	}
	apiRunGit(t, work, "add", "-A")
	commit := exec.Command("git", "commit", "-q", "-m", message)
	commit.Dir = work
	stamp := when.Format(time.RFC3339)
	commit.Env = testfixture.GitEnvironment(append(os.Environ(), "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp))
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, output)
	}
	apiRunGit(t, work, "push", "-q", "--force", "origin", "HEAD:refs/heads/"+branch)
	wroteRefs(app, id)
	return apiGitOutput(t, work, "rev-parse", "HEAD")
}

// warmSnapshot reads the ref snapshot, which pages share and which is read
// again only after a write, so a count that follows covers the page's own
// reads.
func warmSnapshot(t *testing.T, app *App, id string) {
	t.Helper()
	_, err := app.Repositories.RefSnapshot(context.Background(), id)
	noErr(t, err)
}

// A file view lists the file's folder, which names the file's object, and
// reads the file: two Git processes. A folder view lists the folder and reads
// its README. Viewing either again starts none.
func TestFileAndFolderViewsStartTwoGitProcessesAndNoneWhenCached(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	seedRepository(t, app, "views", map[string]string{
		"dir/sub/file.txt":  "first line of the file\n",
		"dir/sub/other.txt": "other\n",
		"dir/README.md":     "# Folder guide\n",
		"top.txt":           "top\n",
	}, when)
	warmSnapshot(t, app, "views")
	server := serve(t, app.Handler())
	client := &http.Client{}
	started := countGit(t, app)

	cases := []struct{ address, content string }{
		{"/repositories/views/code?ref=refs%2Fheads%2Fmain&path=dir%2Fsub%2Ffile.txt", "first line of the file"},
		{"/repositories/views/code?ref=refs%2Fheads%2Fmain&path=dir", "Folder guide"},
	}
	for _, view := range cases {
		body, status := dashboardGET(t, client, server.URL+view.address)
		processes := started()
		if status != http.StatusOK || !strings.Contains(body, view.content) {
			t.Fatalf("GET %s status=%d, content shown=%v", view.address, status, strings.Contains(body, view.content))
		}
		if len(processes) > 2 {
			t.Fatalf("GET %s started %d Git processes, want at most 2: %q", view.address, len(processes), processes)
		}
		body, status = dashboardGET(t, client, server.URL+view.address)
		if processes := started(); status != http.StatusOK || !strings.Contains(body, view.content) || len(processes) != 0 {
			t.Fatalf("repeated GET %s status=%d started %q", view.address, status, processes)
		}
	}
	// The drawer of the file view lists the file's folder from the same read.
	body, _ := dashboardGET(t, client, server.URL+cases[0].address)
	if !strings.Contains(body, "other.txt") {
		t.Fatal("the file view does not list the file's folder")
	}
}

// A commit opened from the list of commits reads its metadata and files with
// one process and its diff with another. Opening it again starts none.
func TestCommitDetailStartsTwoGitProcessesAndNoneWhenCached(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, first := seedRepository(t, app, "history", map[string]string{"a.txt": "alpha\n"}, when)
	commitFiles(t, work, map[string]string{"b.txt": "bravo\n"}, "second", when.Add(time.Hour))
	warmSnapshot(t, app, "history")
	server := serve(t, app.Handler())
	client := &http.Client{}
	started := countGit(t, app)

	list, status := dashboardGET(t, client, server.URL+"/repositories/history/commits?ref=refs%2Fheads%2Fmain")
	if processes := started(); status != http.StatusOK || !strings.Contains(list, "second") || len(processes) != 1 {
		t.Fatalf("commit list status=%d started %q", status, processes)
	}
	address := server.URL + "/repositories/history/commits/" + first + "?ref=refs%2Fheads%2Fmain"
	body, status := dashboardGET(t, client, address)
	processes := started()
	if status != http.StatusOK || !strings.Contains(body, `class="difftable"`) || !strings.Contains(body, "alpha") || strings.Contains(body, "bravo") || !strings.Contains(body, "ref=refs%2Fheads%2Fmain") {
		t.Fatalf("commit status=%d, diff shown=%v", status, strings.Contains(body, "alpha"))
	}
	if len(processes) > 2 {
		t.Fatalf("commit detail started %d Git processes, want at most 2: %q", len(processes), processes)
	}
	if body, status = dashboardGET(t, client, address); status != http.StatusOK || !strings.Contains(body, "alpha") {
		t.Fatalf("repeated commit status=%d", status)
	}
	if processes := started(); len(processes) != 0 {
		t.Fatalf("repeated commit detail started %q", processes)
	}
}

// The file list, the counts and every patch of a commit come from a fixed
// number of processes however many files it changes.
func TestCommitDiffProcessesDoNotGrowWithFiles(t *testing.T) {
	app := newConfiguredApp(t)
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, _ := seedRepository(t, app, "wide", map[string]string{"keep.txt": "keep\n"}, when)
	few := commitFiles(t, work, map[string]string{"f0.txt": "0\n", "f1.txt": "1\n"}, "few", when.Add(time.Hour))
	files := map[string]string{}
	for index := 0; index < 60; index++ {
		files[fmt.Sprintf("m%02d.txt", index)] = fmt.Sprintf("many %d\n", index)
	}
	many := commitFiles(t, work, files, "many", when.Add(2*time.Hour))
	warmSnapshot(t, app, "wide")
	server := serve(t, app.Handler())
	started := countGit(t, app)
	// Both commits are opened from the list, as a reader does.
	dashboardGET(t, &http.Client{}, server.URL+"/repositories/wide/commits")
	started()
	counts := map[string]int{}
	for name, oid := range map[string]string{"few": few, "many": many} {
		body, status := dashboardGET(t, &http.Client{}, server.URL+"/repositories/wide/commits/"+oid)
		counts[name] = len(started())
		if status != http.StatusOK || strings.Count(body, `<table class="difftable">`) != map[string]int{"few": 2, "many": 60}[name] {
			t.Fatalf("%s commit status=%d, diffs=%d", name, status, strings.Count(body, `<table class="difftable">`))
		}
		if name == "many" && strings.Count(body, `<section class="dfile"`) != 60 {
			t.Fatalf("many commit shows %d files", strings.Count(body, `<section class="dfile"`))
		}
	}
	if counts["few"] != counts["many"] || counts["many"] > 2 {
		t.Fatalf("processes for 2 files=%d, for 60 files=%d", counts["few"], counts["many"])
	}
}

// pullRequestRepository makes a repository whose target moved after the
// source branched: the target adds and changes files the source never had.
func pullRequestRepository(t *testing.T, app *App, id string, sourceFiles map[string]string) (work, base, source, target string) {
	t.Helper()
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, base = seedRepository(t, app, id, map[string]string{"shared.txt": "shared\n"}, when)
	apiRunGit(t, work, "checkout", "-q", "-b", "feature")
	source = commitTo(t, app, id, work, "feature", sourceFiles, "feature work", when.Add(time.Hour))
	apiRunGit(t, work, "checkout", "-q", "main")
	target = commitTo(t, app, id, work, "main", map[string]string{"shared.txt": "shared, changed on main\n", "main-only.txt": "main only\n"}, "main moves on", when.Add(2*time.Hour))
	return work, base, source, target
}

// A pull request shows what its source changed since it branched from the
// target, not the difference between the two tips, and reads it with two
// processes, none when cached, however many files changed.
func TestPullRequestComparisonUsesTheMergeBase(t *testing.T) {
	app := newConfiguredApp(t)
	app.PullRequests = &pullrequest.Service{Store: app.Store, Repositories: app.Repositories}
	_, base, source, target := pullRequestRepository(t, app, "compare", map[string]string{"feature.txt": "feature line\n"})
	started := countGit(t, app)

	changes, err := app.comparePullRequestRevisions(context.Background(), "compare", source, target)
	noErr(t, err)
	processes := started()
	if changes.Base != base || changes.Unavailable != "" || changes.PatchesIncomplete || changes.FilesIncomplete ||
		len(changes.Files) != 1 || changes.Files[0].Path != "feature.txt" || len(changes.Files[0].Hunks) == 0 || changes.Files[0].Additions != 1 {
		t.Fatalf("changes=%+v", changes)
	}
	if len(processes) != 2 {
		t.Fatalf("comparison started %d Git processes, want 2: %q", len(processes), processes)
	}
	if again, err := app.comparePullRequestRevisions(context.Background(), "compare", source, target); err != nil || len(again.Files) != 1 || len(started()) != 0 {
		t.Fatalf("cached comparison files=%d err=%v", len(again.Files), err)
	}

	// The page shows the same list and names the merge base.
	server := serve(t, app.Handler())
	body, status := dashboardGET(t, &http.Client{}, server.URL+"/repositories/compare/pull-requests/new?source=feature&target=main")
	if status != http.StatusOK || !strings.Contains(body, "feature.txt") || strings.Contains(body, "main-only.txt") ||
		!strings.Contains(body, "since it branched off the target") || !strings.Contains(body, base[:10]) {
		t.Fatalf("new pull request page status=%d", status)
	}
}

func TestPullRequestComparisonProcessesDoNotGrowWithFiles(t *testing.T) {
	counts := map[int]int{}
	for _, size := range []int{3, 80} {
		app := newConfiguredApp(t)
		files := map[string]string{}
		for index := 0; index < size; index++ {
			files[fmt.Sprintf("dir/f%03d.txt", index)] = fmt.Sprintf("file %d\n", index)
		}
		_, _, source, target := pullRequestRepository(t, app, "grow", files)
		started := countGit(t, app)
		changes, err := app.comparePullRequestRevisions(context.Background(), "grow", source, target)
		noErr(t, err)
		counts[size] = len(started())
		if len(changes.Files) != size || changes.PatchesIncomplete {
			t.Fatalf("%d files: listed %d, incomplete=%v", size, len(changes.Files), changes.PatchesIncomplete)
		}
		for _, file := range changes.Files {
			if len(file.Hunks) != 1 || file.Additions != 1 || !strings.Contains(file.Hunks[0].Lines[0].Text, "file ") {
				t.Fatalf("%s: hunks=%+v", file.Path, file.Hunks)
			}
		}
	}
	if counts[3] != 2 || counts[80] != 2 {
		t.Fatalf("processes for 3 files=%d, for 80 files=%d, want 2", counts[3], counts[80])
	}
}

// Paths with newlines, tabs, quotes, backslashes, spaces before "b/" and
// non-ASCII letters keep their own changes: each file's patch ends where the
// next file's begins.
func TestPullRequestComparisonKeepsUnusualPathsApart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("these file names are not valid on Windows")
	}
	app := newConfiguredApp(t)
	names := []string{"new\nline.txt", "tab\there.txt", `quo"te.txt`, `back\slash.txt`, "space b/x.txt", "é-accent.txt", "plain.txt"}
	files := map[string]string{}
	for index, name := range names {
		files[name] = fmt.Sprintf("content %d\nsecond %d\n", index, index)
	}
	files["binary.bin"] = "\x00\x01\x02binary"
	_, _, source, target := pullRequestRepository(t, app, "names", files)
	changes, err := app.comparePullRequestRevisions(context.Background(), "names", source, target)
	noErr(t, err)
	if len(changes.Files) != len(names)+1 || changes.PatchesIncomplete || changes.FilesIncomplete {
		t.Fatalf("listed %d files: %+v", len(changes.Files), changes)
	}
	byPath := map[string]webui.DiffFile{}
	for _, file := range changes.Files {
		byPath[file.Path] = file
	}
	for index, name := range names {
		file, ok := byPath[name]
		if !ok {
			t.Fatalf("%q is missing from %+v", name, changes.Files)
		}
		if file.Status != "added" || file.Additions != 2 || len(file.Hunks) != 1 || len(file.Hunks[0].Lines) != 2 ||
			file.Hunks[0].Lines[0].Text != fmt.Sprintf("content %d", index) || file.Hunks[0].Lines[1].Text != fmt.Sprintf("second %d", index) {
			t.Fatalf("%q: %+v", name, file)
		}
	}
	if binary := byPath["binary.bin"]; !binary.Binary || len(binary.Hunks) != 0 {
		t.Fatalf("binary file: %+v", binary)
	}
}

// Branches without a merge base, or with several, show why there is no
// comparison instead of comparing tips or picking a base.
func TestPullRequestComparisonWithoutOneMergeBase(t *testing.T) {
	app := newConfiguredApp(t)
	app.PullRequests = &pullrequest.Service{Store: app.Store, Repositories: app.Repositories}
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, base := seedRepository(t, app, "bases", map[string]string{"shared.txt": "shared\n"}, when)

	apiRunGit(t, work, "checkout", "-q", "--orphan", "unrelated")
	apiRunGit(t, work, "rm", "-q", "-rf", ".")
	commitTo(t, app, "bases", work, "unrelated", map[string]string{"alone.txt": "alone\n"}, "unrelated", when.Add(time.Hour))

	// A criss-cross merge: each side merges the other's first commit.
	apiRunGit(t, work, "checkout", "-q", "-f", "-B", "left", base)
	left := commitTo(t, app, "bases", work, "left", map[string]string{"left.txt": "left\n"}, "left", when.Add(2*time.Hour))
	apiRunGit(t, work, "checkout", "-q", "-B", "right", base)
	right := commitTo(t, app, "bases", work, "right", map[string]string{"right.txt": "right\n"}, "right", when.Add(3*time.Hour))
	apiRunGit(t, work, "merge", "-q", "--no-edit", left)
	apiRunGit(t, work, "push", "-q", "--force", "origin", "HEAD:refs/heads/right")
	apiRunGit(t, work, "checkout", "-q", "left")
	apiRunGit(t, work, "merge", "-q", "--no-edit", right)
	apiRunGit(t, work, "push", "-q", "--force", "origin", "HEAD:refs/heads/left")
	wroteRefs(app, "bases")

	server := serve(t, app.Handler())
	client := &http.Client{}
	for _, check := range []struct {
		source, target string
		code           webui.MessageCode
	}{
		{"unrelated", "main", webui.MsgPRChangesNoBase},
		{"left", "right", webui.MsgPRChangesManyBases},
	} {
		body, status := dashboardGET(t, client, server.URL+"/repositories/bases/pull-requests/new?source="+check.source+"&target="+check.target)
		english, korean := webui.Text(webui.LangEN, check.code), webui.Text(webui.LangKO, check.code)
		if status != http.StatusOK || !strings.Contains(body, english) || !strings.Contains(body, korean) || strings.Contains(body, `class="dfile"`) ||
			strings.Contains(body, webui.Text(webui.LangEN, webui.MsgPRChangesNone)) {
			t.Fatalf("%s into %s: status=%d, reason shown=%v", check.source, check.target, status, strings.Contains(body, english))
		}
	}
}

// Every pull request in the list reads its branch heads from one listing.
func TestPullRequestListRefReadsDoNotGrowWithPullRequests(t *testing.T) {
	app := newConfiguredApp(t)
	app.PullRequests = &pullrequest.Service{Store: app.Store, Repositories: app.Repositories}
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	work, _ := seedRepository(t, app, "listed", map[string]string{"base.txt": "base\n"}, when)
	create := func(number int) {
		t.Helper()
		branch := fmt.Sprintf("topic-%d", number)
		apiRunGit(t, work, "checkout", "-q", "-B", branch, "main")
		commitTo(t, app, "listed", work, branch, map[string]string{branch + ".txt": branch + "\n"}, branch, when.Add(time.Duration(number)*time.Hour))
		_, err := app.PullRequests.Create(context.Background(), pullrequest.CreateInput{Repository: "listed", Title: branch, SourceBranch: branch, TargetBranch: "main"})
		noErr(t, err)
	}
	listProcesses := func() int {
		t.Helper()
		started := countGit(t, app)
		views, err := app.PullRequests.List(context.Background(), "listed")
		noErr(t, err)
		for _, view := range views {
			if view.Source.Status != "commit" || view.Target.Status != "commit" || view.Source.OID == view.Target.OID {
				t.Fatalf("pull request %d heads: %+v %+v", view.Number, view.Source, view.Target)
			}
		}
		return len(started())
	}
	create(1)
	one := listProcesses()
	for number := 2; number <= 5; number++ {
		create(number)
	}
	five := listProcesses()
	if one != 1 || five != one {
		t.Fatalf("list of 1 pull request started %d processes, of 5 started %d", one, five)
	}
}

// A line inside a file that looks like the start of another file's patch
// stays in its own file, in a pull request and in a commit, and a commit
// with unusual paths gives each file its own changes.
func TestDiffPatchBoundariesFollowGit(t *testing.T) {
	app := newConfiguredApp(t)
	files := map[string]string{
		"a.txt": "diff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@ -1 +1 @@\n",
		"b.txt": "bee\n",
	}
	if runtime.GOOS != "windows" {
		files["new\nline.txt"] = "diff --git a/b.txt b/b.txt\n"
		files[`q"uote.txt`] = "quote\n"
	}
	_, _, source, target := pullRequestRepository(t, app, "bounds", files)
	changes, err := app.comparePullRequestRevisions(context.Background(), "bounds", source, target)
	noErr(t, err)
	check := func(where string, items []webui.DiffFile) {
		t.Helper()
		if len(items) != len(files) {
			t.Fatalf("%s: %d files, want %d", where, len(items), len(files))
		}
		for _, item := range items {
			want := strings.Split(strings.TrimSuffix(files[item.Path], "\n"), "\n")
			if len(item.Hunks) != 1 || len(item.Hunks[0].Lines) != len(want) {
				t.Fatalf("%s: %q hunks=%+v", where, item.Path, item.Hunks)
			}
			for index, line := range item.Hunks[0].Lines {
				if line.Kind != "add" || line.Text != want[index] {
					t.Fatalf("%s: %q line %d = %+v, want %q", where, item.Path, index, line, want[index])
				}
			}
		}
	}
	check("pull request", changes.Files)

	commit, changed, err := app.Repositories.CommitFiles(context.Background(), "bounds", source)
	noErr(t, err)
	patch, truncated, err := app.Repositories.CommitPatch(context.Background(), "bounds", source, "", nil, maximumCommitPatchBytes)
	noErr(t, err)
	items, notLoaded := diffFileItems(changed, patch, truncated, nil, nil)
	if commit.OID != source || truncated || notLoaded {
		t.Fatalf("commit %s truncated=%v not loaded=%v", commit.OID, truncated, notLoaded)
	}
	check("commit", items)
}
