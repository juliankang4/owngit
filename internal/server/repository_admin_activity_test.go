package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/repository"
)

// TestDeleteStopsActivityCountingAndLeavesNothingCached proves that a
// background activity count does not keep a repository from being deleted,
// and that a new repository with the same name never shows the removed one's
// activity.
func TestDeleteStopsActivityCountingAndLeavesNothingCached(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the delaying wrapper is a Unix test fixture")
	}
	fixture := newAPIFixture(t, false)
	// While the flag file exists, every history walk stalls for 20 s, far
	// beyond the deletion's 2 s lock wait.
	slowFlag := filepath.Join(t.TempDir(), "slow")
	noErr(t, os.WriteFile(slowFlag, nil, 0o600))
	useGitWrapper(t, fixture.app, `if test -e `+serverShellQuote(slowFlag)+`; then for a in "$@"; do if test "$a" = --source; then exec /bin/sleep 20; fi; done; fi`)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)

	fixture.app.StartBackground(context.Background())
	// Wait until the history walk runs and holds the repository's read lock.
	lock := fixture.app.Repositories.Locks.For("project")
	held := func() bool {
		if lock.TryLock() {
			lock.Unlock()
			return false
		}
		return true
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		fixture.app.activity.mu.Lock()
		entry := fixture.app.activity.entries["project"]
		running := entry != nil && entry.running != nil
		fixture.app.activity.mu.Unlock()
		if running && held() {
			time.Sleep(300 * time.Millisecond)
			if held() {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the background count never started")
		}
		time.Sleep(20 * time.Millisecond)
	}

	started := time.Now()
	result := browserForm(t, client, server.URL+"/repositories/project/delete", url.Values{
		"csrf": {adminTestCSRF}, "mode": {"delete_files"}, "confirm_name": {"project"}, "admin_password": {"admin-password"},
	}, server.URL)
	if result.status != http.StatusSeeOther {
		t.Fatalf("delete during a background count status=%d after %v body=%s", result.status, time.Since(started), result.body)
	}
	fixture.app.activity.mu.Lock()
	entry := fixture.app.activity.entries["project"]
	cached := entry != nil && (entry.computed || entry.running != nil)
	fixture.app.activity.mu.Unlock()
	if cached {
		t.Fatal("the deleted repository still has cached or running activity")
	}
	noErr(t, os.Remove(slowFlag))

	// A new repository under the same name, with different history.
	created, err := fixture.app.Repositories.Create(t.Context(), "project", "")
	noErr(t, err)
	remote, err := fixture.app.Repositories.Path(created.ID)
	noErr(t, err)
	work := filepath.Join(t.TempDir(), "reborn")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "API Test")
	apiRunGit(t, work, "config", "user.email", "api-test@example.invalid")
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte("reborn\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", "Reborn repository")
	apiRunGit(t, work, "push", remote, "HEAD:refs/heads/main")

	for _, target := range []string{"/", "/repositories/project"} {
		body := settledGET(t, client, server.URL+target)
		if !strings.Contains(body, "Reborn repository") || !strings.Contains(body, "1 commit in ") || strings.Contains(body, "2 commits in ") {
			t.Fatalf("GET %s does not show only the new repository's activity:\n%s", target, body)
		}
	}
}

// TestDefaultBranchChangeReachesDashboardAndOverview proves that the ref
// snapshot the dashboard and the repository pages read follows a default
// branch change at once. Activity does not depend on the default branch, so
// its cached count stays valid and is not recounted.
func TestDefaultBranchChangeReachesDashboardAndOverview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-counting wrapper is a Unix test fixture")
	}
	fixture := newAPIFixture(t, false)
	tracePath := traceActivityLogs(t, fixture.app)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)

	dashboard := settledGET(t, client, server.URL+"/")
	if row := repositoryRow(t, dashboard); !strings.Contains(row, `title="main"`) || !strings.Contains(dashboard, "2 commits in ") {
		t.Fatalf("dashboard before the change:\n%s", dashboard)
	}
	walks := activityLogCounts(t, tracePath)["project"]["current"]

	result := browserForm(t, client, server.URL+"/repositories/project/settings/default-branch", url.Values{
		"csrf": {adminTestCSRF}, "branch": {"feature"},
	}, server.URL)
	if result.status != http.StatusSeeOther {
		t.Fatalf("default branch change status=%d body=%s", result.status, result.body)
	}

	dashboard = settledGET(t, client, server.URL+"/")
	row := repositoryRow(t, dashboard)
	if !strings.Contains(row, `title="feature"`) || strings.Contains(row, `title="main"`) || !strings.Contains(row, `<span class="row__msg"> feature </span>`) {
		t.Fatalf("dashboard row does not show the new default branch and its tip:\n%s", row)
	}
	if !strings.Contains(dashboard, "2 commits in ") {
		t.Fatalf("dashboard lost the activity count:\n%s", dashboard)
	}
	overview := settledGET(t, client, server.URL+"/repositories/project")
	if !strings.Contains(overview, `<span class="mono">feature</span>`) {
		t.Fatalf("overview does not name the new default branch:\n%s", overview)
	}
	if got := activityLogCounts(t, tracePath)["project"]["current"]; got != walks {
		t.Fatalf("a default branch change recounted activity (%d walks, was %d)", got, walks)
	}
}

// repositoryRow returns the dashboard's first repository row.
func repositoryRow(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `class="row row--repo"`)
	if start < 0 {
		t.Fatalf("no repository row:\n%s", body)
	}
	end := strings.Index(body[start:], "</a>")
	if end < 0 {
		t.Fatal("unterminated repository row")
	}
	// Whitespace from the template is folded, so assertions read as markup.
	return strings.Join(strings.Fields(body[start:start+end]), " ")
}

// TestAutomaticChecksPageLinksBackToSettings proves that the page the
// Settings tab lists links back to it for an administrator, and that a viewer
// without an administrator session never reaches the page.
func TestAutomaticChecksPageLinksBackToSettings(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	target := server.URL + "/repositories/project/configured-checks"
	visitor := browserGET(t, client, target)
	if visitor.status != http.StatusSeeOther || !strings.HasPrefix(visitor.header.Get("Location"), "/admin/login?next=") {
		t.Fatalf("GET without admin status=%d location=%q", visitor.status, visitor.header.Get("Location"))
	}
	signInAdmin(t, fixture, server.URL, jar)
	admin := browserGET(t, client, target)
	if admin.status != http.StatusOK || !strings.Contains(admin.body, `other administrator pages are in its Settings tab:</span> <a href="/repositories/project/settings">`) {
		t.Fatalf("Automatic checks status=%d does not link back to Settings", admin.status)
	}
	settings := browserGET(t, client, server.URL+"/repositories/project/settings")
	if !strings.Contains(settings.body, `href="/repositories/project/configured-checks"`) {
		t.Fatal("Settings does not link to Automatic checks")
	}
}

// TestDefaultBranchChangeWhileInUseIsRefused proves that the change waits only
// briefly for a Git operation holding the repository and then reports it in
// use, instead of outliving the request.
func TestDefaultBranchChangeWhileInUseIsRefused(t *testing.T) {
	fixture := newAPIFixture(t, false)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	lock := fixture.app.Repositories.Locks.For("project")
	lock.RLock()
	started := time.Now()
	result := browserForm(t, client, server.URL+"/repositories/project/settings/default-branch", url.Values{
		"csrf": {adminTestCSRF}, "branch": {"feature"},
	}, server.URL)
	elapsed := time.Since(started)
	lock.RUnlock()
	if result.status != http.StatusConflict || !strings.Contains(result.body, "Another Git operation, such as a push or a clone, is using the repository.") || elapsed > 5*time.Second {
		t.Fatalf("change while in use status=%d after %v", result.status, elapsed)
	}
	summary, err := fixture.app.Repositories.Summary(t.Context(), "project")
	if err != nil || summary.DefaultBranch != "main" {
		t.Fatalf("a refused change moved the default branch to %q err=%v", summary.DefaultBranch, err)
	}
}

// TestDeletionDuringBackgroundCountLeavesLaterRepositoriesCounted proves that
// deleting a repository while the startup count walks it does not stop the
// count of the repositories after it.
func TestDeletionDuringBackgroundCountLeavesLaterRepositoriesCounted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the delaying wrapper is a Unix test fixture")
	}
	app := newConfiguredApp(t)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		addActivityRepository(t, app, name, 2)
	}
	// Only alpha's history walk stalls, so the count stays on it.
	useGitWrapper(t, app, `case "$PWD" in */alpha.git) for a in "$@"; do if test "$a" = --source; then exec /bin/sleep 20; fi; done;; esac`)
	app.StartBackground(context.Background())
	waitForActivity(t, app, "alpha", func(entry *activityEntry) bool { return entry.running != nil })

	// As the deletion page does.
	resume := app.activity.pause("alpha", false)
	_, err := app.Repositories.Delete(t.Context(), "alpha", repository.DeleteFiles)
	resume()
	noErr(t, err)

	for _, name := range []string{"beta", "gamma"} {
		waitForActivity(t, app, name, func(entry *activityEntry) bool { return entry.computed })
	}
}

// waitForActivity waits until id's cache entry satisfies ready.
func waitForActivity(t *testing.T, app *App, id string, ready func(*activityEntry) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		app.activity.mu.Lock()
		entry := app.activity.entries[id]
		done := entry != nil && ready(entry)
		app.activity.mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("activity of %s never reached the expected state", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestDefaultBranchChangeStopsActivityCounting proves that a slow background
// count does not make a default-branch change report the repository in use.
func TestDefaultBranchChangeStopsActivityCounting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the delaying wrapper is a Unix test fixture")
	}
	fixture := newAPIFixture(t, false)
	useGitWrapper(t, fixture.app, `for a in "$@"; do if test "$a" = --source; then exec /bin/sleep 20; fi; done`)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	fixture.app.StartBackground(context.Background())
	waitForActivity(t, fixture.app, "project", func(entry *activityEntry) bool { return entry.running != nil })
	lock := fixture.app.Repositories.Locks.For("project")
	waitUntil(t, func() bool {
		if lock.TryLock() {
			lock.Unlock()
			return false
		}
		return true
	})

	result := browserForm(t, client, server.URL+"/repositories/project/settings/default-branch", url.Values{
		"csrf": {adminTestCSRF}, "branch": {"feature"},
	}, server.URL)
	if result.status != http.StatusSeeOther {
		t.Fatalf("change during a background count status=%d body=%s", result.status, result.body)
	}
	summary, err := fixture.app.Repositories.Summary(t.Context(), "project")
	if err != nil || summary.DefaultBranch != "feature" {
		t.Fatalf("default branch=%q err=%v", summary.DefaultBranch, err)
	}
}

// TestPageDuringDefaultBranchChangeStartsNoCount proves that a page opened
// after a default-branch change stopped the running count, but before the
// change took the write lock, does not start a new count whose read lock would
// make the change report the repository in use.
func TestPageDuringDefaultBranchChangeStartsNoCount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the gating wrapper is a Unix test fixture")
	}
	fixture := newAPIFixture(t, false)
	dir := t.TempDir()
	reached, gate := filepath.Join(dir, "reached"), filepath.Join(dir, "gate")
	noErr(t, os.WriteFile(gate, nil, 0o600))
	// Every history walk stalls for 20 s. The change runs check-ref-format
	// after it stopped counting and before it takes the write lock, so the
	// gate holds the change in exactly that gap.
	useGitWrapper(t, fixture.app, `for a in "$@"; do case "$a" in
--source) exec /bin/sleep 20;;
check-ref-format) : >`+serverShellQuote(reached)+`; while test -e `+serverShellQuote(gate)+`; do /bin/sleep 0.02; done;;
esac; done`)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)

	// A second browser opens the overview inside the gap, as a dashboard in
	// another tab would, and then lets the change continue. It must not call
	// t.Fatal, so it reports through the channel.
	page := make(chan error, 1)
	go func() {
		defer os.Remove(gate)
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(reached); err == nil {
				break
			}
			if time.Now().After(deadline) {
				page <- errors.New("the change never reached check-ref-format")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		response, err := http.Get(server.URL + "/repositories/project")
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				err = fmt.Errorf("overview during the change status=%d", response.StatusCode)
			}
		}
		page <- err
	}()

	result := browserForm(t, client, server.URL+"/repositories/project/settings/default-branch", url.Values{
		"csrf": {adminTestCSRF}, "branch": {"feature"},
	}, server.URL)
	noErr(t, <-page)
	if result.status != http.StatusSeeOther {
		t.Fatalf("change with a page opened meanwhile status=%d body=%s", result.status, result.body)
	}
	summary, err := fixture.app.Repositories.Summary(t.Context(), "project")
	if err != nil || summary.DefaultBranch != "feature" {
		t.Fatalf("default branch=%q err=%v", summary.DefaultBranch, err)
	}
}

// TestDefaultBranchGitFailureIsNotReportedAsAMissingBranch proves that a Git
// failure gives the generic failure sentence, not "That branch does not exist".
func TestDefaultBranchGitFailureIsNotReportedAsAMissingBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failing wrapper is a Unix test fixture")
	}
	fixture := newAPIFixture(t, false)
	useGitWrapper(t, fixture.app, `for a in "$@"; do if test "$a" = show-ref; then echo 'fatal: simulated storage failure' >&2; exit 128; fi; done`)
	server, client, jar := openBrowser(t, fixture)
	signInAdmin(t, fixture, server.URL, jar)
	result := browserForm(t, client, server.URL+"/repositories/project/settings/default-branch", url.Values{
		"csrf": {adminTestCSRF}, "branch": {"feature"},
	}, server.URL)
	if result.status != http.StatusInternalServerError || !strings.Contains(result.body, "The default branch could not be changed.") ||
		strings.Contains(result.body, "That branch does not exist.") || strings.Contains(result.body, "simulated storage failure") {
		t.Fatalf("Git failure status=%d body=%s", result.status, result.body)
	}
}
