package server

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
)

const stillCounting = "Some repositories are still being counted"

// TestActivityCacheFollowsRefChanges proves that pages reuse one observation
// while the counted refs are unchanged and count again after a push through
// Smart HTTP.
func TestActivityCacheFollowsRefChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	app, _, work := newActivityFixture(t, "cached", 2)
	tracePath := traceActivityLogs(t, app)
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	walks := func() int {
		t.Helper()
		return activityLogCounts(t, tracePath)["cached"]["current"]
	}
	for _, target := range []string{"/", "/", "/activity", "/repositories/cached"} {
		if body, status := dashboardGET(t, client, server.URL+target); status != http.StatusOK || strings.Contains(body, stillCounting) {
			t.Fatalf("GET %s status=%d counting=%v", target, status, strings.Contains(body, stillCounting))
		}
	}
	if got := walks(); got != 1 {
		t.Fatalf("unchanged refs were walked %d times, want once", got)
	}

	apiRunGit(t, work, "tag", "uncounted")
	remote := server.URL + "/git/cached.git"
	apiRunGit(t, work, "push", remote, "refs/tags/uncounted")
	dashboardGET(t, client, server.URL+"/")
	if got := walks(); got != 1 {
		t.Fatalf("a tag push walked history again (%d walks)", got)
	}

	noErr(t, os.WriteFile(filepath.Join(work, "fresh.txt"), []byte("fresh\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "Counted after the push")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main")
	body, status := dashboardGET(t, client, server.URL+"/")
	if status != http.StatusOK || !strings.Contains(body, "Counted after the push") || !strings.Contains(body, "3 commits in ") {
		t.Fatalf("dashboard after push status=%d did not show the new commit and total", status)
	}
	if got := walks(); got != 2 {
		t.Fatalf("a branch push produced %d walks, want 2", got)
	}
}

// TestDashboardRendersWhileActivityIsStillCounting proves that slow history
// never holds the page: it renders at once with an explicit counting state and
// shows the complete count once counting finishes.
func TestDashboardRendersWhileActivityIsStillCounting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the delaying wrapper is a Unix test fixture")
	}
	app, _, _ := newActivityFixture(t, "slow-history", 1)
	useGitWrapper(t, app, `for a in "$@"; do if test "$a" = --source; then /bin/sleep 2; fi; done`)
	app.activity.wait = 50 * time.Millisecond
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	started := time.Now()
	body, status := dashboardGET(t, client, server.URL+"/")
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("dashboard waited %v for slow history", elapsed)
	}
	if status != http.StatusOK || !strings.Contains(body, stillCounting) || !strings.Contains(body, "slow-history") {
		t.Fatalf("dashboard status=%d counting=%v", status, strings.Contains(body, stillCounting))
	}
	deadline := time.Now().Add(20 * time.Second)
	for strings.Contains(body, stillCounting) {
		if time.Now().After(deadline) {
			t.Fatal("activity was still counting after 20 seconds")
		}
		time.Sleep(100 * time.Millisecond)
		body, status = dashboardGET(t, client, server.URL+"/")
	}
	if status != http.StatusOK || !strings.Contains(body, "1 commit in ") || strings.Contains(body, "This count is incomplete") {
		t.Fatalf("finished dashboard status=%d did not show the complete count", status)
	}
}

// TestPagesReplyBeforeTheRequestDeadline reproduces storage slower than the
// request deadline. Every page must still answer before the deadline, with an
// error page where it cannot read the repository, instead of an empty reply
// after the write deadline.
func TestPagesReplyBeforeTheRequestDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the delaying wrapper is a Unix test fixture")
	}
	for target, want := range map[string]int{
		"/":                             http.StatusServiceUnavailable,
		"/activity":                     http.StatusOK,
		"/repositories/stalled":         http.StatusServiceUnavailable,
		"/repositories/stalled/commits": http.StatusServiceUnavailable,
	} {
		t.Run(target, func(t *testing.T) {
			app, _, _ := newActivityFixture(t, "stalled", 1)
			useGitWrapper(t, app, `for a in "$@"; do case "$a" in for-each-ref|symbolic-ref|rev-parse|log) exec /bin/sleep 30;; esac; done`)
			app.HTTPTimeout = 2 * time.Second
			server := serve(t, app.Handler())
			jar, _ := cookiejar.New(nil)
			client := &http.Client{Jar: jar}
			started := time.Now()
			body, status := dashboardGET(t, client, server.URL+target)
			if elapsed := time.Since(started); elapsed > app.HTTPTimeout {
				t.Fatalf("reply took %v, beyond the %v request deadline", elapsed, app.HTTPTimeout)
			}
			if status != want || body == "" {
				t.Fatalf("stalled storage status=%d bytes=%d, want status %d with a page", status, len(body), want)
			}
		})
	}
}

// TestRepositoryPageDoesNotReuseTruncatedDashboardCount proves that the
// repository page counts with its own full budget instead of reusing the
// dashboard's observation cut short by the page-wide budget.
func TestRepositoryPageDoesNotReuseTruncatedDashboardCount(t *testing.T) {
	app := newConfiguredApp(t)
	app.ActivityLimit = 4
	addActivityRepository(t, app, "a", 3)
	addActivityRepository(t, app, "b", 3)
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if body := settledGET(t, client, server.URL+"/"); !strings.Contains(body, "This count is incomplete") {
		t.Fatal("the dashboard was not incomplete at the page-wide limit")
	}
	body := settledGET(t, client, server.URL+"/repositories/b")
	if strings.Contains(body, "This count is incomplete") || !strings.Contains(body, "3 commits in ") {
		t.Fatal("the repository page reused the dashboard's truncated observation")
	}
}

// TestStopBackgroundCancelsSlowCounting proves that shutdown does not wait
// for a slow history walk to finish.
func TestStopBackgroundCancelsSlowCounting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the delaying wrapper is a Unix test fixture")
	}
	app, _, _ := newActivityFixture(t, "slow", 1)
	useGitWrapper(t, app, `for a in "$@"; do if test "$a" = --source; then exec /bin/sleep 20; fi; done`)
	app.StartBackground(context.Background())
	time.Sleep(700 * time.Millisecond)
	started := time.Now()
	app.StopBackground()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("StopBackground took %v with a slow walk in flight", elapsed)
	}
}

// settledGET repeats a GET until the page no longer reports counting.
func settledGET(t *testing.T, client *http.Client, target string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		body, status := dashboardGET(t, client, target)
		if status != http.StatusOK {
			t.Fatalf("GET %s status=%d", target, status)
		}
		if !strings.Contains(body, stillCounting) && !strings.Contains(body, "This repository is still being counted") {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s was still counting after 20 seconds", target)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestActivityCacheMemoryStaysWithinTwiceTheLimit proves that cached
// observations are dropped, least recently used first, once they exceed twice
// the page-wide limit.
func TestActivityCacheMemoryStaysWithinTwiceTheLimit(t *testing.T) {
	app := newConfiguredApp(t)
	app.ActivityLimit = 2
	for _, name := range []string{"one", "two", "three"} {
		addActivityRepository(t, app, name, 3)
	}
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	for _, name := range []string{"one", "two", "three"} {
		if _, status := dashboardGET(t, client, server.URL+"/repositories/"+name); status != http.StatusOK {
			t.Fatalf("repository %s status=%d", name, status)
		}
	}
	app.activity.mu.Lock()
	defer app.activity.mu.Unlock()
	cached := 0
	for id, entry := range app.activity.entries {
		if entry.computed {
			cached += len(entry.activity.Records)
			if id == "one" {
				t.Fatal("the least recently used observation was kept")
			}
		}
	}
	if cached != app.activity.records || cached > 2*app.ActivityLimit {
		t.Fatalf("cache holds %d records (tracked %d), limit %d", cached, app.activity.records, 2*app.ActivityLimit)
	}
}

// useGitWrapper runs prelude before every Git invocation of the app.
func useGitWrapper(t *testing.T, app *App, prelude string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "git")
	noErr(t, os.WriteFile(wrapper, []byte("#!/bin/sh\n"+prelude+"\nexec "+serverShellQuote(gitPath)+" \"$@\"\n"), 0o700))
	runner, err := gitexec.New(wrapper, filepath.Join(dir, "runtime"))
	noErr(t, err)
	app.Repositories.Git = runner
}

// TestDropKeepsRecordAccounting proves that dropping a counted repository, as
// a pause for a deletion does, subtracts its records, so the cache's count
// keeps matching what it holds and later evictions stay correct.
func TestDropKeepsRecordAccounting(t *testing.T) {
	app := newConfiguredApp(t)
	for _, name := range []string{"one", "two"} {
		addActivityRepository(t, app, name, 3)
	}
	server := serve(t, app.Handler())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	for _, name := range []string{"one", "two"} {
		if _, status := dashboardGET(t, client, server.URL+"/repositories/"+name); status != http.StatusOK {
			t.Fatalf("repository %s status=%d", name, status)
		}
	}
	app.activity.pause("one", false)()
	app.activity.mu.Lock()
	defer app.activity.mu.Unlock()
	cached := 0
	for _, entry := range app.activity.entries {
		if entry.computed {
			cached += len(entry.activity.Records)
		}
	}
	if _, kept := app.activity.entries["one"]; kept || cached != app.activity.records || cached == 0 {
		t.Fatalf("after drop: entry kept=%v, cache holds %d records, tracked %d", kept, cached, app.activity.records)
	}
}

// TestResultOfADroppedCountIsDiscarded proves that a count that completes
// after its repository was dropped neither returns to the cache nor changes
// the record count.
func TestResultOfADroppedCountIsDiscarded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the delaying wrapper is a Unix test fixture")
	}
	app, _, _ := newActivityFixture(t, "late", 3)
	// The history walk waits until the flag file is removed.
	flag := filepath.Join(t.TempDir(), "hold")
	noErr(t, os.WriteFile(flag, nil, 0o600))
	useGitWrapper(t, app, `for a in "$@"; do if test "$a" = --source; then while test -e `+serverShellQuote(flag)+`; do /bin/sleep 0.02; done; fi; done`)
	snapshot, err := app.Repositories.RefSnapshot(t.Context(), "late")
	noErr(t, err)

	cache := &app.activity
	cache.mu.Lock()
	cache.initLocked()
	cache.capacity = 1000
	done := cache.scheduleLocked(app.Repositories, "late", snapshot.ActivityKey, 1000)
	cache.mu.Unlock()
	lock := app.Repositories.Locks.For("late")
	waitUntil(t, func() bool {
		if lock.TryLock() {
			lock.Unlock()
			return false
		}
		return true
	})
	// Hold the cache while the walk finishes successfully, so the result is
	// ready before the drop and must be discarded, not cancelled.
	cache.mu.Lock()
	noErr(t, os.Remove(flag))
	waitUntil(t, func() bool {
		if lock.TryLock() {
			lock.Unlock()
			return true
		}
		return false
	})
	cache.dropLocked("late")
	cache.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the dropped count never finished")
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if _, kept := cache.entries["late"]; kept || cache.records != 0 {
		t.Fatalf("a dropped count came back: entry kept=%v records=%d", kept, cache.records)
	}
}

// waitUntil polls condition for up to 10 seconds.
func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within 10 seconds")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPausedRepositoryIsNotCounted proves that a pause for a deletion forgets
// the finished observation, one for a default-branch change keeps it, neither
// lets planning schedule a count or recreate an entry, and counting stays
// paused until the last overlapping pause resumes, even when one resume is
// called twice.
func TestPausedRepositoryIsNotCounted(t *testing.T) {
	cache := &activityCache{}
	cache.mu.Lock()
	cache.initLocked()
	for _, id := range []string{"deleted", "renamed"} {
		entry := cache.entryLocked(id)
		entry.activity = repository.Activity{Key: "key", Records: []repository.ActivityRecord{{OID: id}}}
		entry.limit, entry.computed = 10, true
		cache.records++
	}
	cache.mu.Unlock()

	resumeDeleted := cache.pause("deleted", false)
	resumeRenamed := cache.pause("renamed", true)
	resumeAgain := cache.pause("renamed", true)
	cache.mu.Lock()
	parts, pending := cache.planLocked(nil, []string{"deleted", "renamed"}, []string{"key", "other-key"}, 10, map[string]bool{})
	_, recreated := cache.entries["deleted"]
	cache.mu.Unlock()
	if !parts[0].pending || len(parts[0].records) != 0 || recreated {
		t.Fatalf("paused deletion part=%+v entry recreated=%v", parts[0], recreated)
	}
	// The kept observation is shown while it is still pending, because its
	// key no longer matches, but no count starts for it.
	if !parts[1].pending || len(parts[1].records) != 1 || len(pending) != 0 || cache.records != 1 {
		t.Fatalf("paused change part=%+v pending=%d records=%d", parts[1], len(pending), cache.records)
	}

	resumeDeleted()
	resumeRenamed()
	resumeRenamed()
	cache.mu.Lock()
	still := cache.paused["renamed"]
	cache.mu.Unlock()
	resumeAgain()
	if still != 1 || len(cache.paused) != 0 {
		t.Fatalf("paused after one resume=%d, after all=%v", still, cache.paused)
	}
}

// TestDroppedRepositoryIsReportedAsStillCounting proves that a repository
// dropped after a page scheduled it is reported as still being counted, not
// as having no activity, because the change that dropped it may have been
// refused and the repository may still exist.
func TestDroppedRepositoryIsReportedAsStillCounting(t *testing.T) {
	cache := &activityCache{}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.initLocked()
	parts, pending := cache.planLocked(nil, []string{"dropped"}, []string{"key"}, 10, map[string]bool{"dropped": true})
	if !parts[0].pending || len(parts[0].records) != 0 || len(pending) != 0 {
		t.Fatalf("dropped repository part=%+v pending=%d", parts[0], len(pending))
	}
	if _, created := cache.entries["dropped"]; created {
		t.Fatal("planning recreated the dropped repository's entry")
	}
}
