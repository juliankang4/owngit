package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/webui"
)

func TestRepositoryHomeProcessCounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the tracing wrapper is a POSIX-shell fixture")
	}
	app, remote, work := newActivityFixture(t, "reuse", 2)
	apiRunGit(t, work, "tag", "-a", "old-release", "-m", "old-release", "HEAD~1")
	apiRunGit(t, work, "tag", "-a", "release", "-m", "release")
	apiRunGit(t, work, "push", remote, "refs/tags/old-release", "refs/tags/release")
	apiRunGit(t, work, "push", remote, ":refs/tags/old-release")
	tracePath := traceGitCommands(t, app)
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	visit := func(label string) (tipReads, peels, listings int) {
		t.Helper()
		before, err := os.ReadFile(tracePath)
		noErr(t, err)
		started := time.Now()
		body, status := dashboardGET(t, client, server.URL+"/repositories/reuse")
		elapsed := time.Since(started)
		if status != http.StatusOK || !strings.Contains(body, ">release<") || !strings.Contains(body, "Kept history") {
			t.Fatalf("%s page status=%d did not show refs and retained history", label, status)
		}
		after, err := os.ReadFile(tracePath)
		noErr(t, err)
		commands := after[len(before):]
		tipReads = bytes.Count(commands, []byte("\x00--no-walk\x00"))
		peels = bytes.Count(commands, []byte("\x00--batch-check="))
		listings = bytes.Count(commands, []byte("\x00for-each-ref\x00"))
		t.Logf("home_read label=%s latency_ms=%.3f git_processes=%d tip_metadata=%d tag_peels=%d ref_listings=%d", label, float64(elapsed)/float64(time.Millisecond), bytes.Count(commands, []byte{'\n'}), tipReads, peels, listings)
		return
	}
	visit("first")
	for index := 0; index < 3; index++ {
		tips, peels, listings := visit("warm")
		if tips != 0 || peels != 0 || listings != 0 {
			t.Errorf("warm home started tip=%d peel=%d listing=%d Git processes", tips, peels, listings)
		}
	}
	noErr(t, os.WriteFile(filepath.Join(work, "fresh.txt"), []byte("fresh\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "fresh home revision")
	apiRunGit(t, work, "push", server.URL+"/git/reuse.git", "HEAD:refs/heads/main")
	visit("post_push")
	body, status := dashboardGET(t, client, server.URL+"/repositories/reuse")
	if status != http.StatusOK || !strings.Contains(body, "fresh home revision") {
		t.Fatal("post-push home did not show the new commit")
	}
}

func TestRepositoryHomePinsListTipsHeadAndReadme(t *testing.T) {
	app, remote, work := newActivityFixture(t, "pinned", 1)
	noErr(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("old readme\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "old home")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main")
	ctx := context.Background()
	snapshot, err := app.Repositories.RefSnapshot(ctx, "pinned")
	noErr(t, err)
	stored, _, err := app.Store.Repository(ctx, "pinned")
	noErr(t, err)
	fill := func(wantSubject, wantReadme string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/repositories/pinned", nil)
		page := app.baseRepositoryPage(request, webui.Chrome{}, stored, snapshot.Summary)
		noErr(t, app.fillRepositoryOverview(request, &page, snapshot, ""))
		if page.Ref.Revision != snapshot.Summary.DefaultOID || page.Overview.Head.Subject != wantSubject || page.Overview.Readme == nil || !strings.Contains(string(page.Overview.Readme.Rendered), wantReadme) {
			t.Fatalf("revision=%s head=%s README=%+v", page.Ref.Revision, page.Overview.Head.Subject, page.Overview.Readme)
		}
		if len(page.Overview.Branches) != len(snapshot.Summary.Branches) || page.Overview.Branches[0].Tip.OID != snapshot.Summary.DefaultOID {
			t.Fatal("branch list and tip are not from the same snapshot")
		}
	}
	fill("old home", "old readme")
	noErr(t, os.WriteFile(filepath.Join(work, "README.md"), []byte("new readme\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "new home")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main")
	fill("old home", "old readme")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/new")
	// Simulate an OwnGit writer completing between listing and selection.
	lock := app.Repositories.Locks.For("pinned")
	lock.Lock()
	lock.Unlock()
	fill("old home", "old readme")
	for _, selection := range []string{"refs/heads/new", "refs/heads/missing"} {
		request := httptest.NewRequest(http.MethodGet, "/repositories/pinned", nil)
		page := app.baseRepositoryPage(request, webui.Chrome{}, stored, snapshot.Summary)
		noErr(t, app.fillRepositoryOverview(request, &page, snapshot, selection))
		if selection == "refs/heads/new" && (page.Ref.Missing || page.Overview.Head.Subject != "new home") {
			t.Fatal("a requested ref outside the snapshot changed its selection rule")
		}
		if selection == "refs/heads/missing" && (!page.Ref.Missing || page.Ref.IsDefault) {
			t.Fatal("a missing requested ref silently fell back to the default branch")
		}
	}
	snapshot, err = app.Repositories.RefSnapshot(ctx, "pinned")
	noErr(t, err)
	fill("new home", "new readme")
}

func TestRepositoryWriteRecountsWithoutPageRequests(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the activity trace wrapper is a POSIX-shell fixture")
	}
	app, _, work := newActivityFixture(t, "recount", 1)
	trace := traceActivityLogs(t, app)
	app.StartBackground(context.Background())
	settled := func(commits int) bool {
		app.activity.mu.Lock()
		defer app.activity.mu.Unlock()
		entry := app.activity.entries["recount"]
		return entry != nil && entry.running == nil && entry.computed && entry.activity.Commits == commits
	}
	waitUntil(t, func() bool { return settled(1) })
	app.GitHTTP.OnReceive = app.NoteRepositoryChange
	app.Repositories.OnChange = app.NoteRepositoryChange
	app.PullRequests.OnChange = app.NoteRepositoryChange
	server := serve(t, app.Handler())
	noErr(t, os.WriteFile(filepath.Join(work, "fresh.txt"), []byte("fresh\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "background recount")
	started := time.Now()
	apiRunGit(t, work, "push", server.URL+"/git/recount.git", "HEAD:refs/heads/main")
	waitUntil(t, func() bool { return settled(2) })
	t.Logf("background recount settled without a GET in %v", time.Since(started))
	if walks := activityLogCounts(t, trace)["recount"]["current"]; walks != 2 {
		t.Fatalf("walks=%d, want startup and post-push", walks)
	}
	jar, _ := cookiejar.New(nil)
	body, status := dashboardGET(t, &http.Client{Jar: jar}, server.URL+"/")
	if status != http.StatusOK || strings.Contains(body, stillCounting) || !strings.Contains(body, "2 commits in ") {
		t.Fatal("the dashboard did not find the finished post-write count")
	}
}

func TestRepositoryRecountWaitsForWriteGeneration(t *testing.T) {
	app, remote, work := newActivityFixture(t, "ordered", 1)
	app.StartBackground(context.Background())
	waitUntil(t, func() bool {
		app.activity.mu.Lock()
		defer app.activity.mu.Unlock()
		entry := app.activity.entries["ordered"]
		return entry != nil && entry.computed && entry.running == nil
	})
	noErr(t, os.WriteFile(filepath.Join(work, "fresh.txt"), []byte("fresh\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "count after unlock")
	lock := app.Repositories.Locks.For("ordered")
	lock.Lock()
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main")
	// Receive callbacks run before the handler releases its write lock.
	app.NoteRepositoryChange("ordered")
	app.activity.mu.Lock()
	done := app.activity.entries["ordered"].running
	app.activity.mu.Unlock()
	finishedBeforeUnlock := false
	select {
	case <-done:
		finishedBeforeUnlock = true
	case <-time.After(50 * time.Millisecond):
	}
	lock.Unlock()
	if finishedBeforeUnlock {
		t.Fatal("recount reused the old key before the write generation advanced")
	}
	waitUntil(t, func() bool {
		app.activity.mu.Lock()
		defer app.activity.mu.Unlock()
		entry := app.activity.entries["ordered"]
		return entry.running == nil && entry.activity.Commits == 2
	})
}

func TestRepositoryRecountsCoalesceWhilePausedAndCancelAtShutdown(t *testing.T) {
	app, _, _ := newActivityFixture(t, "pending", 1)
	app.activity.bind(context.Background())
	resume := app.activity.pause("pending", true)
	for index := 0; index < 100; index++ {
		app.NoteRepositoryChange("pending")
	}
	app.activity.mu.Lock()
	pending := len(app.activity.recounts)
	app.activity.mu.Unlock()
	if pending != 1 {
		t.Fatalf("pending recounts=%d, want one", pending)
	}
	// Reserve every existing worker slot to leave the resumed recount queued.
	for index := 0; index < activityConcurrency; index++ {
		app.activity.slots <- struct{}{}
	}
	resume()
	for index := 0; index < 100; index++ {
		app.NoteRepositoryChange("pending")
	}
	app.activity.mu.Lock()
	pending = len(app.activity.recounts)
	running := app.activity.entries["pending"].running != nil
	app.activity.mu.Unlock()
	if pending != 1 || !running {
		t.Fatalf("pending=%d running=%v, want one of each", pending, running)
	}
	started := time.Now()
	app.StopBackground()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shutdown took %v for a queued recount", elapsed)
	}
	app.NoteRepositoryChange("pending")
	app.activity.mu.Lock()
	pending = len(app.activity.recounts)
	app.activity.mu.Unlock()
	if pending != 0 {
		t.Fatal("shutdown retained or accepted a pending recount")
	}
}
