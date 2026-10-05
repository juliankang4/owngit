package tray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"owngit/internal/server"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// feedFixture is a fake server whose state directory is a real one, so the
// notifier keeps its cursor there, and a notifier that records what it
// shows.
type feedFixture struct {
	*fakeServer
	held     *os.File
	notifier *notifier
	shown    []string
	// handed are the notifications of the reads the notifier handed over, one
	// entry per read; took is how many of each read the platform takes, and
	// failOn makes showing that notification fail once.
	handed [][]string
	took   int
	failOn string
	now    time.Time
}

func newFeedFixture(t *testing.T) *feedFixture {
	t.Helper()
	fake := newFakeServer(t)
	fake.stateDir = filepath.Join(t.TempDir(), "state")
	held, err := state.CreateDirectory(fake.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Close() })
	fake.publish(t, fake.URL, fake.token)
	fixture := &feedFixture{fakeServer: fake, held: held, now: time.Now()}
	fixture.notifier = &notifier{stateDir: fake.stateDir, client: NewClient(fake.stateDir, nil), lang: webui.LangEN, show: fixture.show}
	return fixture
}

// show is the platform: it takes at most took notifications of one read, and
// fails on failOn instead of taking it.
func (fixture *feedFixture) show(notifications []server.TrayNotification) (int, error) {
	took := fixture.took
	if took == 0 || took > len(notifications) {
		took = len(notifications)
	}
	fixture.handed = append(fixture.handed, ids(notifications))
	shown := 0
	for _, notification := range notifications[:took] {
		if notification.ID == fixture.failOn {
			fixture.failOn = ""
			return shown, errors.New("the notification service is away")
		}
		fixture.shown = append(fixture.shown, notification.ID)
		shown++
	}
	return shown, nil
}

// notify reads the feed once, and the next read happens a minute later, as the
// poller's reads do.
func (fixture *feedFixture) notify(ctx context.Context) {
	fixture.notifier.notify(ctx, fixture.now)
	fixture.now = fixture.now.Add(time.Minute)
}

func ids(notifications []server.TrayNotification) []string {
	names := make([]string, 0, len(notifications))
	for _, notification := range notifications {
		names = append(names, notification.ID)
	}
	return names
}

func answerEvents(cursor string, notifications ...server.TrayNotification) func(http.ResponseWriter) {
	return func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(server.TrayEvents{OK: true, Cursor: cursor, Notifications: append([]server.TrayNotification{}, notifications...)})
	}
}

func pushNotification(id string) server.TrayNotification {
	return server.TrayNotification{ID: id, Kind: state.NotifyPush, Title: "2 new commits in notes", Path: "/repositories/notes/commits?ref=main"}
}

// lastQuery is the query of the fake server's last request.
func (fixture *feedFixture) lastQuery(t *testing.T) url.Values {
	t.Helper()
	last := fixture.requests[len(fixture.requests)-1]
	parsed, err := url.Parse(strings.Fields(last)[0])
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}

func (fixture *feedFixture) cursor(t *testing.T) string {
	t.Helper()
	cursor, err := state.ReadTrayCursor(fixture.held)
	if err != nil {
		t.Fatal(err)
	}
	return cursor
}

// The cursor moves only once every notification of an answer is shown; a
// notification shown before a failure is not shown again, and the next read
// sends the kept cursor with the owner's choices.
func TestNotifierKeepsTheCursorOnceShown(t *testing.T) {
	fixture := newFeedFixture(t)
	fixture.answer = answerEvents("c1", pushNotification("push:1-1"), pushNotification("push:2-2"))
	fixture.failOn = "push:2-2"
	fixture.notify(context.Background())
	if query := fixture.lastQuery(t); query.Has("cursor") || query.Get("kinds") != strings.Join(state.NotifyKinds, ",") || query.Get("only_others") != "0" || query.Get("lang") != "en" {
		t.Fatalf("first read asked %v", query)
	}
	if !slices.Equal(fixture.shown, []string{"push:1-1"}) || fixture.cursor(t) != "" {
		t.Fatalf("after a failed show: shown %v, cursor %q", fixture.shown, fixture.cursor(t))
	}
	fixture.notify(context.Background())
	if !slices.Equal(fixture.shown, []string{"push:1-1", "push:2-2"}) || fixture.cursor(t) != "c1" {
		t.Fatalf("after the retry: shown %v, cursor %q", fixture.shown, fixture.cursor(t))
	}

	if err := state.WriteTrayNotifications(fixture.held, state.TrayNotifications{OnlyOthers: true, KindsOff: []string{state.NotifyPush}}); err != nil {
		t.Fatal(err)
	}
	fixture.answer = answerEvents("c2")
	// A new icon process reads the same cursor.
	fixture.notifier = &notifier{stateDir: fixture.stateDir, client: NewClient(fixture.stateDir, nil), lang: webui.LangKO, show: fixture.show}
	fixture.notify(context.Background())
	query := fixture.lastQuery(t)
	if query.Get("cursor") != "c1" || query.Get("only_others") != "1" || strings.Contains(query.Get("kinds"), state.NotifyPush) || query.Get("lang") != "ko" || fixture.cursor(t) != "c2" {
		t.Fatalf("second read asked %v, cursor %q", query, fixture.cursor(t))
	}
}

// A platform that shows one notification at a time takes one of a read: the
// icon hands over the next one at its next read, records none as shown before
// the platform took it, and moves the cursor only once the read is shown.
func TestNotifierHandsOverWhatThePlatformTakes(t *testing.T) {
	fixture := newFeedFixture(t)
	// The icon reads from the cursor it kept before, and keeps it until every
	// notification of the read is shown.
	if err := state.WriteTrayCursor(fixture.held, "c1"); err != nil {
		t.Fatal(err)
	}
	fixture.took = 1
	fixture.answer = answerEvents("c2", pushNotification("push:1-1"), pushNotification("push:2-2"), pushNotification("push:3-3"))
	fixture.notify(context.Background())
	if !slices.Equal(fixture.shown, []string{"push:1-1"}) || fixture.cursor(t) != "c1" {
		t.Fatalf("after one taken: shown %v, cursor %q", fixture.shown, fixture.cursor(t))
	}
	fixture.notify(context.Background())
	if !slices.Equal(fixture.shown, []string{"push:1-1", "push:2-2"}) || fixture.cursor(t) != "c1" {
		t.Fatalf("after two taken: shown %v, cursor %q", fixture.shown, fixture.cursor(t))
	}
	fixture.notify(context.Background())
	if !slices.Equal(fixture.shown, []string{"push:1-1", "push:2-2", "push:3-3"}) || fixture.cursor(t) != "c2" {
		t.Fatalf("after the last one: shown %v, cursor %q", fixture.shown, fixture.cursor(t))
	}
	// Every read handed over the notifications the icon had not shown, once
	// each: nothing is shown twice.
	want := [][]string{{"push:1-1", "push:2-2", "push:3-3"}, {"push:2-2", "push:3-3"}, {"push:3-3"}}
	if !slices.EqualFunc(fixture.handed, want, slices.Equal) {
		t.Fatalf("the reads handed over %v, want %v", fixture.handed, want)
	}
}

// A desktop without a notification service does not keep the icon asking on
// every read: it waits longer each time, says so in the panel, and follows the
// wait.
func TestNotifierWaitsLongerWithoutANotificationService(t *testing.T) {
	fixture := newFeedFixture(t)
	fixture.answer = answerEvents("c1", pushNotification("push:1-1"))
	asked := 0
	fixture.notifier.show = func([]server.TrayNotification) (int, error) {
		asked++
		return 0, fmt.Errorf("%w: the name is not activatable", errNoNotificationService)
	}
	at := fixture.now
	fixture.notifier.notify(context.Background(), at)
	if asked != 1 || fixture.cursor(t) != "" || fixture.notifier.unavailable(webui.LangEN) == "" {
		t.Fatalf("after the first failure: %d reads asked, cursor %q, panel %q", asked, fixture.cursor(t), fixture.notifier.unavailable(webui.LangEN))
	}
	// A read before the wait is over does not ask the desktop again.
	fixture.notifier.notify(context.Background(), at.Add(time.Second))
	if asked != 1 {
		t.Fatalf("asked again before the wait: %d reads asked", asked)
	}
	// A read after it does, and the wait became longer.
	waited := fixture.notifier.retryAt.Sub(at)
	fixture.notifier.notify(context.Background(), fixture.notifier.retryAt)
	if asked != 2 || fixture.notifier.retryAt.Sub(at) <= waited {
		t.Fatalf("after the wait: %d reads asked, wait %v then %v", asked, waited, fixture.notifier.retryAt.Sub(at))
	}
	// Each failure waits longer than the one before, up to the longest.
	if retryWait(1) != notifyRetryFirst || retryWait(2) != 2*notifyRetryFirst || retryWait(20) != notifyRetryLongest {
		t.Fatalf("waits %v, %v, %v", retryWait(1), retryWait(2), retryWait(20))
	}
	// A notification shown once clears the state and the wait, also while the
	// rest of the read still waits for its turn.
	fixture.answer = answerEvents("c1", pushNotification("push:1-1"), pushNotification("push:2-2"))
	showing := 0
	fixture.notifier.show = func([]server.TrayNotification) (int, error) {
		showing++
		return 1, nil
	}
	fixture.notifier.notify(context.Background(), at.Add(notifyRetryLongest))
	if showing != 1 || fixture.cursor(t) != "" || fixture.notifier.unavailable(webui.LangEN) != "" || !fixture.notifier.retryAt.IsZero() {
		t.Fatalf("after a shown notification: %d reads asked, cursor %q, panel %q, retry %v", showing, fixture.cursor(t), fixture.notifier.unavailable(webui.LangEN), fixture.notifier.retryAt)
	}
	// The notification the platform did not take yet is shown at the next read,
	// and then the cursor moves.
	fixture.notifier.show = func(notifications []server.TrayNotification) (int, error) {
		return len(notifications), nil
	}
	fixture.notifier.notify(context.Background(), at.Add(notifyRetryLongest))
	if fixture.cursor(t) != "c1" {
		t.Fatalf("after the rest of the read: cursor %q", fixture.cursor(t))
	}
}

// A hidden icon shows nothing and keeps no cursor; a cursor that cannot be
// read starts again from now; an answer that is not the feed shows nothing.
func TestNotifierShowsNothingItShouldNot(t *testing.T) {
	fixture := newFeedFixture(t)
	if err := state.WriteTrayCursor(fixture.held, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := state.SetTrayHidden(fixture.held, true); err != nil {
		t.Fatal(err)
	}
	fixture.answer = answerEvents("c2", pushNotification("push:1-1"))
	fixture.notify(context.Background())
	if len(fixture.shown) != 0 || fixture.cursor(t) != "" {
		t.Fatalf("hidden: shown %v, cursor %q", fixture.shown, fixture.cursor(t))
	}
	if err := state.SetTrayHidden(fixture.held, false); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(fixture.stateDir, state.TrayCursorFile), []byte("not a cursor"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.answer = answerEvents("c3")
	fixture.notify(context.Background())
	if fixture.lastQuery(t).Has("cursor") || fixture.cursor(t) != "c3" {
		t.Fatalf("damaged cursor: asked %v, cursor %q", fixture.lastQuery(t), fixture.cursor(t))
	}

	for name, reply := range map[string]func(http.ResponseWriter){
		"another address": answerEvents("c4", server.TrayNotification{ID: "x", Kind: state.NotifyPush, Path: "//example.com/"}),
		"no path":         answerEvents("c4", server.TrayNotification{ID: "x", Kind: state.NotifyPush, Path: "https://example.com/"}),
		"unknown kind":    answerEvents("c4", server.TrayNotification{ID: "x", Kind: "webhook", Path: "/"}),
		"no cursor":       answerEvents("", pushNotification("push:9-9")),
		"not OK":          answer(http.StatusOK, "application/json", `{"ok":false,"cursor":"c4","notifications":[]}`),
	} {
		fixture.answer = reply
		fixture.notify(context.Background())
		if len(fixture.shown) != 0 || fixture.cursor(t) != "c3" {
			t.Errorf("%s: shown %v, cursor %q", name, fixture.shown, fixture.cursor(t))
		}
	}
}

// A notification opens its page on the dashboard only after the server
// proves its address, and only a page of that dashboard.
func TestDashboardPage(t *testing.T) {
	fake := newFakeServer(t)
	client := NewClient(fake.stateDir, nil)
	page, err := client.DashboardPage(context.Background(), "en", "/repositories/notes/commits?ref=main")
	if err != nil || page != fake.URL+"/repositories/notes/commits?ref=main" {
		t.Fatalf("page %q %v", page, err)
	}
	for _, path := range []string{"//example.com/", "https://example.com/", "", "/a\\b"} {
		if page, err := client.DashboardPage(context.Background(), "en", path); err == nil {
			t.Errorf("%q opened %q", path, page)
		}
	}
	fake.proof = "another-proof"
	if page, err := client.DashboardPage(context.Background(), "en", "/"); err == nil {
		t.Errorf("an unproven answer opened %q", page)
	}
}

// A click opens the page of the balloon Windows reports clicked, and a click
// after that balloon is gone opens nothing: the page of an earlier notification
// is never reused, also when Windows does not report a balloon gone.
func TestClickPagesFollowTheBalloonWindowsReports(t *testing.T) {
	push := server.TrayNotification{Kind: state.NotifyPush, Path: "/repositories/notes/commits?ref=main"}
	pull := server.TrayNotification{Kind: state.NotifyPullRequest, Path: "/repositories/notes/pull-requests/1"}
	var pages clickPages
	if page := pages.clicked(); page != "" {
		t.Fatalf("a click without a balloon opens %q", page)
	}
	pages.add(pull)
	if page := pages.clicked(); page != pull.Path {
		t.Fatalf("the balloon of the pull request opens %q", page)
	}
	if page := pages.clicked(); page != "" {
		t.Fatalf("a click after the balloon is gone opens %q", page)
	}
	// The balloon handed over next is the one on screen, so it is the one a
	// click opens, whatever was shown before it.
	pages.add(push)
	pages.add(pull)
	if page := pages.clicked(); page != pull.Path {
		t.Fatalf("the balloon on screen opens %q, want %q", page, pull.Path)
	}
	// Windows reported a balloon gone without a click: the page of the balloon
	// after it is gone with it, so a click opens nothing.
	pages.add(push)
	pages.gone()
	if page := pages.clicked(); page != "" {
		t.Fatalf("a click after a balloon was dismissed opens %q", page)
	}
}

// One balloon stands for one read: the notification itself while the read holds
// few enough of them, and otherwise one summary that counts them all and opens
// the dashboard's home page. Every balloon carries text, because a balloon
// without a body removes the notification instead of showing it.
func TestBalloonForOneRead(t *testing.T) {
	notifications := []server.TrayNotification{pushNotification("a"), pushNotification("b"), pushNotification("c"), pushNotification("d")}
	balloon, taken := balloonFor(notifications[:summaryLimit], webui.LangKO)
	if taken != 1 || balloon.ID != notifications[0].ID || balloon.Path != notifications[0].Path {
		t.Fatalf("%d notifications took %d, balloon %+v", summaryLimit, taken, balloon)
	}
	balloon, taken = balloonFor(notifications, webui.LangKO)
	if taken != len(notifications) || balloon.Path != "/" || balloon.Title == "" || balloon.Body == "" {
		t.Fatalf("%d notifications took %d, balloon %+v", len(notifications), taken, balloon)
	}
	if count := fmt.Sprintf("%d", len(notifications)); !strings.Contains(balloon.Title, count) {
		t.Fatalf("the balloon for %d notifications is %q", len(notifications), balloon.Title)
	}
	// The server sends summaries that have no body of their own; the balloon
	// still has text the desktop shows.
	title, body := balloonText(server.TrayNotification{Title: "4 pushes"})
	if title == "" || body == "" {
		t.Fatalf("a notification without a body is shown as %q / %q", title, body)
	}
}
