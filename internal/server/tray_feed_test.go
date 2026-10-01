package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/githttp"
	"owngit/internal/pullrequest"
	"owngit/internal/state"
)

// feedClock is the clock of a feed test; pushes read it on the server's
// goroutines.
type feedClock struct{ unix atomic.Int64 }

func (clock *feedClock) set(at time.Time)         { clock.unix.Store(at.Unix()) }
func (clock *feedClock) now() time.Time           { return time.Unix(clock.unix.Load(), 0) }
func (clock *feedClock) add(offset time.Duration) { clock.set(clock.now().Add(offset)) }

// feedRead reads the event feed of app after cursor with the kinds, the
// "only what I did not do" choice and the language of query.
func feedRead(t *testing.T, app *App, cursor string, query url.Values) TrayEvents {
	t.Helper()
	if query == nil {
		query = url.Values{}
	}
	if !query.Has("kinds") {
		query.Set("kinds", strings.Join(state.NotifyKinds, ","))
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	code, body, errorCode := trayRead(t, app.Handler(), TrayEventsPath, trayTestToken, func(request *http.Request) {
		request.URL.RawQuery = query.Encode()
	})
	if code != http.StatusOK {
		t.Fatalf("event feed: %d %q", code, errorCode)
	}
	var events TrayEvents
	noErr(t, json.Unmarshal(body, &events))
	if !events.OK || !state.ValidTrayCursor(events.Cursor) || events.Notifications == nil {
		t.Fatalf("event feed answered %s", body)
	}
	return events
}

// feedApp is an installation with the repositories notes and site, a clock
// the test sets, and a work tree that pushes to it.
func feedApp(t *testing.T) (*App, *feedClock, string, string) {
	t.Helper()
	app, _, _, server := releaseApp(t, "v1.0.2")
	app.TrayToken, app.TrayProof = trayTestToken, trayTestProof
	app.GitHTTP.OnPush = app.RecordPush
	clock := &feedClock{}
	clock.set(time.Unix(1_900_000_000, 0))
	app.Now, app.PullRequests.Now = clock.now, clock.now
	app.Sleep = clock.add
	for _, name := range []string{"notes", "site"} {
		_, err := app.Repositories.Create(context.Background(), name, "")
		noErr(t, err)
	}
	work := filepath.Join(t.TempDir(), "work")
	apiRunGit(t, "", "init", "--initial-branch=main", work)
	apiRunGit(t, work, "config", "user.name", "Feed Test")
	apiRunGit(t, work, "config", "user.email", "feed@example.invalid")
	return app, clock, server.URL, work
}

func commit(t *testing.T, work, message string) string {
	t.Helper()
	noErr(t, os.WriteFile(filepath.Join(work, "file.txt"), []byte(message+"\n"), 0o600))
	apiRunGit(t, work, "add", ".")
	apiRunGit(t, work, "commit", "-m", message)
	return apiGitOutput(t, work, "rev-parse", "HEAD")
}

// onlyNotification returns the one notification of events.
func onlyNotification(t *testing.T, events TrayEvents) TrayNotification {
	t.Helper()
	if len(events.Notifications) != 1 {
		t.Fatalf("notifications %+v, want one", events.Notifications)
	}
	return events.Notifications[0]
}

// A push is reported once, a minute after OwnGit received it, with its
// commits, and pushes within that minute of the first are reported
// together. A cursor that was not kept gets the same answer again.
func TestTrayEventsReportPushesOnceAMinuteLater(t *testing.T) {
	app, clock, address, work := feedApp(t)
	start := feedRead(t, app, "", nil)
	if !start.Started || len(start.Notifications) != 0 {
		t.Fatalf("first read: %+v", start)
	}

	commit(t, work, "first")
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/main")
	clock.add(time.Minute)
	created := feedRead(t, app, start.Cursor, nil)
	if got := onlyNotification(t, created); got.Title != "New branch main in notes" || got.Body != "first" {
		t.Fatalf("new branch %+v", got)
	}

	commit(t, work, "Monthly budget")
	commit(t, work, "Add weekly review template")
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/main")
	clock.add(59 * time.Second)
	waiting := feedRead(t, app, created.Cursor, nil)
	if len(waiting.Notifications) != 0 || waiting.Started {
		t.Fatalf("within the minute: %+v", waiting)
	}
	clock.add(time.Second)
	report := feedRead(t, app, waiting.Cursor, nil)
	if got := onlyNotification(t, report); got != (TrayNotification{
		ID: "push:2-2", Kind: state.NotifyPush, Title: "2 new commits in notes", Body: "main: Add weekly review template and 1 more",
		Path: "/repositories/notes/commits?ref=main",
	}) {
		t.Fatalf("push notification %+v", got)
	}
	if again := feedRead(t, app, waiting.Cursor, nil); len(again.Notifications) != 1 || again.Notifications[0].ID != "push:2-2" {
		t.Fatalf("a cursor that was not kept: %+v", again)
	}
	if after := feedRead(t, app, report.Cursor, nil); len(after.Notifications) != 0 {
		t.Fatalf("reported twice: %+v", after)
	}
	korean := feedRead(t, app, waiting.Cursor, url.Values{"lang": {"ko"}})
	if got := onlyNotification(t, korean); got.Title != "notes에 새 커밋 2개" || got.Body != "main: Add weekly review template 외 1개" {
		t.Fatalf("Korean notification %+v", got)
	}

	// Three pushes within a minute, and one after it.
	cursor := report.Cursor
	commit(t, work, "third")
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/main")
	clock.add(20 * time.Second)
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/work")
	clock.add(20 * time.Second)
	apiRunGit(t, work, "push", address+"/git/site.git", "HEAD:refs/heads/main")
	clock.add(30 * time.Second)
	commit(t, work, "September expenses")
	apiRunGit(t, work, "push", address+"/git/site.git", "HEAD:refs/heads/main")
	clock.add(30 * time.Second)
	grouped := feedRead(t, app, cursor, nil)
	if got := onlyNotification(t, grouped); got.ID != "push:3-5" || got.Title != "3 pushes" || got.Path != "/activity" ||
		got.Body != "notes 2, site 1. Latest: site main, third" {
		t.Fatalf("grouped notification %+v", got)
	}
	clock.add(30 * time.Second)
	last := feedRead(t, app, grouped.Cursor, nil)
	if got := onlyNotification(t, last); got.ID != "push:6-6" || got.Title != "1 new commit in site" || got.Body != "main: September expenses" {
		t.Fatalf("the push after the minute %+v", got)
	}

	// A new branch and a deleted one say so, as a tag does.
	cursor = last.Cursor
	apiRunGit(t, work, "push", address+"/git/site.git", "HEAD:refs/heads/feature")
	clock.add(time.Minute)
	branches := feedRead(t, app, cursor, nil)
	if got := onlyNotification(t, branches); got.Title != "New branch feature in site" || got.Body != "September expenses" {
		t.Fatalf("new branch %+v", got)
	}
	apiRunGit(t, work, "push", address+"/git/site.git", ":refs/heads/feature")
	clock.add(time.Minute)
	deleted := feedRead(t, app, branches.Cursor, nil)
	if got := onlyNotification(t, deleted); got.Title != "feature deleted in site" || got.Path != "/repositories/site" {
		t.Fatalf("deleted branch %+v", got)
	}
	apiRunGit(t, work, "tag", "v1")
	apiRunGit(t, work, "push", address+"/git/site.git", "refs/tags/v1")
	clock.add(time.Minute)
	if got := onlyNotification(t, feedRead(t, app, deleted.Cursor, nil)); got.Title != "New tag v1 in site" || got.Path != "/repositories/site/commits?ref=v1" {
		t.Fatalf("new tag %+v", got)
	}
}

// With "only what I did not do", pushes from this computer are dropped and
// the rest say where they came from. What this process did not see, such
// as a push before a restart, counts as from elsewhere and is shown.
func TestTrayEventsDropWhatThisComputerDid(t *testing.T) {
	app, clock, address, work := feedApp(t)
	start := feedRead(t, app, "", nil)
	first := commit(t, work, "first")
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/main")
	second := commit(t, work, "second")
	// The commit arrives without a push event, as if another computer
	// pushed it.
	app.GitHTTP.OnPush = nil
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/elsewhere")
	app.GitHTTP.OnPush = app.RecordPush
	elsewhere := httptest.NewRequest(http.MethodPost, "http://owngit.example:7654/git/notes.git/git-receive-pack", nil)
	elsewhere.RemoteAddr = "192.0.2.10:50000"
	app.RecordPush(elsewhere, "notes", []githttp.RefUpdate{{Ref: "refs/heads/main", Old: first, New: second}})
	clock.add(time.Minute)

	others := url.Values{"only_others": {"1"}}
	got := onlyNotification(t, feedRead(t, app, start.Cursor, others))
	if got.ID != "push:2-2" || got.Subtitle != "Pushed from another computer" || got.Title != "1 new commit in notes" || got.Body != "main: second" {
		t.Fatalf("only others %+v", got)
	}
	if got := onlyNotification(t, feedRead(t, app, start.Cursor, nil)); got.Title != "2 pushes" || got.Subtitle != "" {
		t.Fatalf("every push %+v", got)
	}
	korean := url.Values{"only_others": {"1"}, "lang": {"ko"}}
	if got := onlyNotification(t, feedRead(t, app, start.Cursor, korean)); got.Subtitle != "다른 컴퓨터에서 푸시" {
		t.Fatalf("Korean origin %+v", got)
	}
	app.trayOrigins = trayOrigins{}
	if got := feedRead(t, app, start.Cursor, others); len(got.Notifications) != 1 || got.Notifications[0].Title != "2 pushes" {
		t.Fatalf("after a restart %+v", got.Notifications)
	}
}

func TestTrustedLoopbackPushFilteringUsesObservedLocality(t *testing.T) {
	app, clock, address, work := feedApp(t)
	app.Network = NewLiveNetwork(LiveNetworkConfig{
		Proxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, Hosts: app.Hosts,
	})
	others := url.Values{"only_others": {"1"}}
	start := feedRead(t, app, "", others)

	first := commit(t, work, "first")
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/main")
	second := commit(t, work, "second")
	app.GitHTTP.OnPush = nil
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/elsewhere")
	app.GitHTTP.OnPush = app.RecordPush
	forwarded := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/git/notes.git/git-receive-pack", nil)
	forwarded.RemoteAddr = "127.0.0.1:50000"
	forwarded.Header.Set("X-Forwarded-For", "192.0.2.10")
	app.Network.Resolver().Middleware(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		app.RecordPush(request, "notes", []githttp.RefUpdate{{Ref: "refs/heads/main", Old: first, New: second}})
	})).ServeHTTP(httptest.NewRecorder(), forwarded)
	clock.add(time.Minute)

	if !app.trayOrigins.fromThisComputer(pushOrigin(1)) {
		t.Fatal("a direct loopback push was not recorded as this computer when loopback was trusted")
	}
	if app.trayOrigins.fromThisComputer(pushOrigin(2)) {
		t.Fatal("a push carrying forwarding data was recorded as this computer")
	}
	got := onlyNotification(t, feedRead(t, app, start.Cursor, others))
	if got.ID != "push:2-2" || got.Subtitle != "Pushed from another computer" || got.Title != "1 new commit in notes" || got.Body != "main: second" {
		t.Fatalf("only others %+v", got)
	}
	if got := onlyNotification(t, feedRead(t, app, start.Cursor, nil)); got.Title != "2 pushes" || got.Subtitle != "" {
		t.Fatalf("every push %+v", got)
	}
}

// A kind that is off is dropped for good, a cursor of records this server
// does not have starts over, and a malformed request is refused.
func TestTrayEventsCursorRules(t *testing.T) {
	app, clock, address, work := feedApp(t)
	start := feedRead(t, app, "", nil)
	commit(t, work, "first")
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/main")
	clock.add(time.Minute)
	off := feedRead(t, app, start.Cursor, url.Values{"kinds": {""}})
	if len(off.Notifications) != 0 {
		t.Fatalf("all off: %+v", off)
	}
	if later := feedRead(t, app, off.Cursor, nil); len(later.Notifications) != 0 {
		t.Fatalf("turned on again, the push was reported: %+v", later)
	}

	times := map[string]int64{}
	for _, kind := range trayRecordKinds {
		times[kind] = clock.now().Unix()
	}
	replaced := trayCursor{Push: 99, Times: times, Update: ""}.encode()
	if again := feedRead(t, app, replaced, nil); !again.Started || len(again.Notifications) != 0 {
		t.Fatalf("a cursor past the last push %+v", again)
	}
	if again := feedRead(t, app, "e30", nil); !again.Started {
		t.Fatalf("a cursor without a time %+v", again)
	}
	delete(times, state.NotifyBackupFailed)
	if again := feedRead(t, app, trayCursor{Push: 1, Times: times}.encode(), nil); !again.Started {
		t.Fatalf("a cursor without the time of a kind %+v", again)
	}

	for name, query := range map[string]url.Values{
		"unknown kind":         {"kinds": {"push,webhook"}},
		"only others":          {"only_others": {"yes"}},
		"cursor not base64url": {"cursor": {"not a cursor"}},
	} {
		code, _, errorCode := trayRead(t, app.Handler(), TrayEventsPath, trayTestToken, func(request *http.Request) { request.URL.RawQuery = query.Encode() })
		if code != http.StatusBadRequest || errorCode != "invalid_request" {
			t.Errorf("%s: %d %q", name, code, errorCode)
		}
	}
}

// Pull requests, failed checks, imports and backups are reported a minute
// after their records were written, one by one or counted when there are
// many, and a newer release once, with its command.
func TestTrayEventsReportOtherKinds(t *testing.T) {
	app, clock, address, work := feedApp(t)
	app.UpdateCommand = func(version string) (string, string, bool) { return "brew upgrade owngit", "", false }
	commit(t, work, "first")
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/main", "HEAD:refs/heads/feature", "HEAD:refs/heads/draft")
	commit(t, work, "second")
	apiRunGit(t, work, "push", address+"/git/notes.git", "HEAD:refs/heads/feature", "HEAD:refs/heads/draft")
	start := feedRead(t, app, "", url.Values{"kinds": {"pull_request,import_failed,update"}})
	clock.add(time.Second)

	// Opened from this computer through the API, and by a request OwnGit
	// did not see.
	created := apiRequest(t, http.MethodPost, address+"/api/v1/repositories/notes/pull-requests", map[string]any{
		"title": "Weekly review", "source_branch": "feature", "target_branch": "main", "review": "skip",
	}, "", "")
	if created.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d", created.StatusCode)
	}
	created.Body.Close()
	_, err := app.PullRequests.Create(context.Background(), pullrequest.CreateInput{
		Repository: "notes", Title: "Draft notes", SourceBranch: "draft", TargetBranch: "main", ReviewChoice: "skip", Actor: generalAccessActor,
	})
	noErr(t, err)
	noErr(t, app.Store.Exec(context.Background(), `INSERT INTO import_runs(id,repository_id,source_generation,authority_revision,kind,status,started_at,finished_at,message,created_at) VALUES('run-1','site',1,1,'scheduled','failed',?,?,'the source did not answer',?)`,
		clock.now().Unix(), clock.now().Unix(), clock.now().Unix()))
	query := url.Values{"kinds": {"pull_request,import_failed,update"}}
	if early := feedRead(t, app, start.Cursor, query); len(early.Notifications) != 0 {
		t.Fatalf("before a minute passed: %+v", early.Notifications)
	}
	clock.add(time.Minute)
	report := feedRead(t, app, start.Cursor, query)
	want := []TrayNotification{
		{ID: "pull_request:notes/1", Kind: state.NotifyPullRequest, Title: "Pull request #1 opened in notes", Body: "Weekly review", Path: "/repositories/notes/pull-requests/1"},
		{ID: "pull_request:notes/2", Kind: state.NotifyPullRequest, Title: "Pull request #2 opened in notes", Body: "Draft notes", Path: "/repositories/notes/pull-requests/2"},
		{ID: "import_failed:run-1", Kind: state.NotifyImportFailed, Title: "Import did not finish in site", Body: "the source did not answer", Path: "/repositories/site/import"},
	}
	if len(report.Notifications) != len(want) {
		t.Fatalf("notifications %+v", report.Notifications)
	}
	for index, notification := range report.Notifications {
		if notification != want[index] {
			t.Errorf("notification %d = %+v, want %+v", index, notification, want[index])
		}
	}
	others := url.Values{"kinds": {"pull_request"}, "only_others": {"1"}, "lang": {"ko"}}
	if got := onlyNotification(t, feedRead(t, app, start.Cursor, others)); got.Title != "notes에 풀 리퀘스트 #2 열림" || got.Subtitle != "다른 컴퓨터에서 열림" {
		t.Fatalf("only others %+v", got)
	}
	if again := feedRead(t, app, report.Cursor, query); len(again.Notifications) != 0 {
		t.Fatalf("reported twice: %+v", again.Notifications)
	}

	// More than a few of one kind become one notification.
	for index := range 4 {
		noErr(t, app.Store.Exec(context.Background(), `INSERT INTO import_runs(id,repository_id,source_generation,authority_revision,kind,status,started_at,finished_at,created_at) VALUES(?,'site',1,1,'scheduled','interrupted',?,?,?)`,
			"run-many-"+string(rune('a'+index)), clock.now().Unix(), clock.now().Unix(), clock.now().Unix()))
	}
	clock.add(time.Minute)
	many := feedRead(t, app, report.Cursor, query)
	if got := onlyNotification(t, many); got.Title != "4 imports did not finish" || got.Path != "/activity" {
		t.Fatalf("many imports %+v", got)
	}

	// A newer release, once.
	app.Releases.URL = newReleaseURL(t, "v1.0.3")
	noErr(t, app.Releases.Check(context.Background()))
	update := feedRead(t, app, many.Cursor, query)
	if got := onlyNotification(t, update); got.ID != "update:1.0.3" || got.Title != "OwnGit 1.0.3 is available" || got.Body != "To update, run this command: brew upgrade owngit" || got.Path != "/" {
		t.Fatalf("update %+v", got)
	}
	if again := feedRead(t, app, update.Cursor, query); len(again.Notifications) != 0 {
		t.Fatalf("update reported twice: %+v", again.Notifications)
	}
	// A feed that starts while a release is known does not report it.
	if fresh := feedRead(t, app, "", query); len(fresh.Notifications) != 0 || len(feedRead(t, app, fresh.Cursor, query).Notifications) != 0 {
		t.Fatal("a known release was reported to a feed that started after it")
	}
}

// newReleaseURL is the address of a release endpoint that answers tag.
func newReleaseURL(t *testing.T, tag string) string {
	t.Helper()
	_, address := newReleaseEndpoint(t, tag)
	return address
}

// failedImport records an import of site that did not finish at at.
func failedImport(t *testing.T, app *App, id string, at time.Time) {
	t.Helper()
	noErr(t, app.Store.Exec(context.Background(), `INSERT INTO import_runs(id,repository_id,source_generation,authority_revision,kind,status,started_at,finished_at,message,created_at) VALUES(?,'site',1,1,'scheduled','failed',?,?,'the source did not answer',?)`,
		id, at.Unix(), at.Unix(), at.Unix()))
}

// What was recorded before the feed started, or before the icon was shown
// again (hiding removes its cursor), is never reported; what follows is.
func TestTrayEventsStartAfterWhatCameBefore(t *testing.T) {
	app, clock, _, _ := feedApp(t)
	failedImport(t, app, "before-start", clock.now().Add(-30*time.Second))
	start := feedRead(t, app, "", nil)
	clock.add(time.Second)
	failedImport(t, app, "after-start", clock.now())
	clock.add(time.Minute)
	first := feedRead(t, app, start.Cursor, nil)
	if got := onlyNotification(t, first); got.ID != "import_failed:after-start" {
		t.Fatalf("after the start %+v", got)
	}

	// Hidden, then shown again with no cursor, next to an import.
	failedImport(t, app, "while-hidden", clock.now().Add(-10*time.Second))
	shown := feedRead(t, app, "", nil)
	if !shown.Started || len(shown.Notifications) != 0 {
		t.Fatalf("shown again %+v", shown)
	}
	clock.add(2 * time.Minute)
	if later := feedRead(t, app, shown.Cursor, nil); len(later.Notifications) != 0 {
		t.Fatalf("an import from while hidden was reported: %+v", later.Notifications)
	}
}

// subsecondClock is a clock for tests that need parts of a second; the
// feed's wait for its starting point moves it.
func subsecondClock(app *App, at time.Time) *time.Time {
	app.Now = func() time.Time { return at }
	app.Sleep = func(wait time.Duration) { at = at.Add(wait) }
	return &at
}

// A starting point is the next whole second, and the feed answers once it
// came: records of the second the feed started in, written before it
// answered, came before the start, and records written after the answer
// are new.
func TestTrayEventsStartAtTheNextSecond(t *testing.T) {
	app, clock, _, _ := feedApp(t)
	second := clock.now()
	at := subsecondClock(app, second.Add(100*time.Millisecond))
	for index := range 4 {
		failedImport(t, app, "before-"+strconv.Itoa(index), *at)
	}
	start := feedRead(t, app, "", nil)
	answered := *at
	*at = at.Add(200 * time.Millisecond)
	failedImport(t, app, "after", *at)
	*at = at.Add(2 * time.Minute)
	first := feedRead(t, app, start.Cursor, nil)
	if got := onlyNotification(t, first); got.ID != "import_failed:after" {
		t.Fatalf("after four imports in the start's second %+v", got)
	}

	// A kind that is off starts again at the next second too.
	*at = at.Add(300 * time.Millisecond)
	failedImport(t, app, "while-off", *at)
	off := feedRead(t, app, first.Cursor, url.Values{"kinds": {"pull_request"}})
	*at = at.Add(200 * time.Millisecond)
	failedImport(t, app, "on-again", *at)
	*at = at.Add(2 * time.Minute)
	on := feedRead(t, app, off.Cursor, nil)
	if got := onlyNotification(t, on); got.ID != "import_failed:on-again" {
		t.Fatalf("after imports were off %+v", got)
	}
	if !answered.Equal(second.Add(time.Second)) {
		t.Fatalf("the first answer came at %v, want the next second", answered)
	}
	// A read with every kind on does not wait.
	before := *at
	feedRead(t, app, on.Cursor, nil)
	if !at.Equal(before) {
		t.Fatalf("a read with every kind on waited %v", at.Sub(before))
	}
}

// Failed checks keep nanoseconds: those finished in the second the feed
// started in, before it answered, are not reported, nor those of the
// second before; those after the answer are.
func TestTrayEventsStartTellsChecksApartWithinTheSecond(t *testing.T) {
	app, clock, _, _ := feedApp(t)
	ctx, second := context.Background(), clock.now()
	_, err := app.Store.SetCheckPolicy(ctx, state.CheckPolicyInput{
		RepositoryID: "notes", Executor: state.CheckExecutorExternalRunner, AllowedEvents: []string{"push"},
		MaxTimeoutMS: 60_000, MaxOutputLimitBytes: 64 << 10, QueueLimit: 4, MaxActiveJobs: 1, MaxLeaseMS: 60_000,
	}, second)
	noErr(t, err)
	_, err = app.Store.GrantCheckConsent(ctx, "notes", second)
	noErr(t, err)
	fail := func(name string, finished time.Time) {
		t.Helper()
		job, _, err := app.Store.AdmitCheckJob(ctx, state.CheckJobRequest{
			RepositoryID: "notes", Trigger: "push", EventKey: "refs/heads/" + name + "@" + strings.Repeat("a", 40),
			SourceOID: strings.Repeat("a", 40), TriggerRef: name, WorkflowDigest: strings.Repeat("b", 64),
			Checks: []state.CheckDefinition{{Name: "unit", Command: "go test ./..."}},
		}, second.Add(-time.Minute))
		noErr(t, err)
		noErr(t, app.Store.Exec(ctx, `UPDATE check_jobs SET status = 'failed', finished_at = ? WHERE id = ?`, finished.UnixNano(), job.ID))
	}
	at := subsecondClock(app, second.Add(400*time.Millisecond))
	fail("second-before", second.Add(-500*time.Millisecond))
	for index := range 4 {
		fail("before-start-"+strconv.Itoa(index), second.Add(200*time.Millisecond))
	}
	start := feedRead(t, app, "", nil)
	fail("after-start", at.Add(100*time.Millisecond))
	*at = at.Add(2 * time.Minute)
	if got := onlyNotification(t, feedRead(t, app, start.Cursor, nil)); got.Subtitle != "after-start" {
		t.Fatalf("checks in the start's second %+v", got)
	}
}

// Records written while their kind, or all notifications, were off are
// dropped even when the kind is on again before they are a minute old,
// while a kind that stays on keeps its records that are not yet due.
func TestTrayEventsOffDropsWhatCameMeanwhile(t *testing.T) {
	app, clock, _, _ := feedApp(t)
	start := feedRead(t, app, "", nil)
	clock.add(time.Second)
	failedImport(t, app, "while-all-off", clock.now())
	clock.add(10 * time.Second)
	off := feedRead(t, app, start.Cursor, url.Values{"kinds": {""}})
	clock.add(time.Minute)
	if got := feedRead(t, app, off.Cursor, nil); len(got.Notifications) != 0 {
		t.Fatalf("an import from while all were off: %+v", got.Notifications)
	}

	// Imports off, pull requests on: the pull request due later stays.
	start = feedRead(t, app, off.Cursor, nil)
	clock.add(time.Second)
	failedImport(t, app, "while-imports-off", clock.now())
	_, err := app.Store.CreatePullRequest(context.Background(), "notes", "Weekly review", "feature", "main", strings.Repeat("a", 40), strings.Repeat("b", 40), state.ReviewNotRequested, clock.now())
	noErr(t, err)
	clock.add(10 * time.Second)
	importsOff := feedRead(t, app, start.Cursor, url.Values{"kinds": {"pull_request"}})
	if len(importsOff.Notifications) != 0 {
		t.Fatalf("before a minute passed: %+v", importsOff.Notifications)
	}
	clock.add(time.Minute)
	if got := onlyNotification(t, feedRead(t, app, importsOff.Cursor, nil)); got.ID != "pull_request:notes/1" {
		t.Fatalf("with imports on again %+v", got)
	}
}

// "Only what I did not do" counts only what came from elsewhere, also
// beyond the records a read shows one by one.
func TestTrayEventsOnlyOthersCountsWhatCameFromElsewhere(t *testing.T) {
	app, clock, _, _ := feedApp(t)
	local := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/import", nil)
	local.RemoteAddr = "127.0.0.1:12345"
	others := url.Values{"only_others": {"1"}}
	start := feedRead(t, app, "", others)
	mine := func(round string) {
		for index := range 104 {
			id := round + "-" + strconv.Itoa(index)
			failedImport(t, app, id, clock.now())
			app.trayOrigins.note(local, originKey(state.NotifyImportFailed, id))
		}
	}
	clock.add(time.Second)
	mine("first")
	clock.add(time.Minute)
	first := feedRead(t, app, start.Cursor, others)
	if len(first.Notifications) != 0 {
		t.Fatalf("only this computer's imports: %+v", first.Notifications)
	}
	mine("second")
	failedImport(t, app, "remote", clock.now())
	clock.add(time.Minute)
	if got := onlyNotification(t, feedRead(t, app, first.Cursor, others)); got.ID != "import_failed:remote" {
		t.Fatalf("an import from elsewhere after 104 of this computer's %+v", got)
	}
}
