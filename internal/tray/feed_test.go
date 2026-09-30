package tray

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
	// failOn makes showing that notification fail once.
	failOn string
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
	fixture := &feedFixture{fakeServer: fake, held: held}
	fixture.notifier = &notifier{stateDir: fake.stateDir, client: NewClient(fake.stateDir, nil), lang: webui.LangEN, show: func(notification server.TrayNotification) error {
		if notification.ID == fixture.failOn {
			fixture.failOn = ""
			return errors.New("the notification service is away")
		}
		fixture.shown = append(fixture.shown, notification.ID)
		return nil
	}}
	return fixture
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
	fixture.notifier.notify(context.Background())
	if query := fixture.lastQuery(t); query.Has("cursor") || query.Get("kinds") != strings.Join(state.NotifyKinds, ",") || query.Get("only_others") != "0" || query.Get("lang") != "en" {
		t.Fatalf("first read asked %v", query)
	}
	if !slices.Equal(fixture.shown, []string{"push:1-1"}) || fixture.cursor(t) != "" {
		t.Fatalf("after a failed show: shown %v, cursor %q", fixture.shown, fixture.cursor(t))
	}
	fixture.notifier.notify(context.Background())
	if !slices.Equal(fixture.shown, []string{"push:1-1", "push:2-2"}) || fixture.cursor(t) != "c1" {
		t.Fatalf("after the retry: shown %v, cursor %q", fixture.shown, fixture.cursor(t))
	}

	if err := state.WriteTrayNotifications(fixture.held, state.TrayNotifications{OnlyOthers: true, KindsOff: []string{state.NotifyPush}}); err != nil {
		t.Fatal(err)
	}
	fixture.answer = answerEvents("c2")
	// A new icon process reads the same cursor.
	fixture.notifier = &notifier{stateDir: fixture.stateDir, client: NewClient(fixture.stateDir, nil), lang: webui.LangKO, show: fixture.notifier.show}
	fixture.notifier.notify(context.Background())
	query := fixture.lastQuery(t)
	if query.Get("cursor") != "c1" || query.Get("only_others") != "1" || strings.Contains(query.Get("kinds"), state.NotifyPush) || query.Get("lang") != "ko" || fixture.cursor(t) != "c2" {
		t.Fatalf("second read asked %v, cursor %q", query, fixture.cursor(t))
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
	fixture.notifier.notify(context.Background())
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
	fixture.notifier.notify(context.Background())
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
		fixture.notifier.notify(context.Background())
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

// A click that Windows cannot attribute opens a page that covers every
// notification shown so far.
func TestClickPagesCoverEveryNotificationShown(t *testing.T) {
	push := func(path string) server.TrayNotification {
		return server.TrayNotification{Kind: state.NotifyPush, Path: path}
	}
	pull := server.TrayNotification{Kind: state.NotifyPullRequest, Path: "/repositories/notes/pull-requests/1"}
	for _, test := range []struct {
		name  string
		shown []server.TrayNotification
		want  string
	}{
		{"one", []server.TrayNotification{pull}, pull.Path},
		{"one page twice", []server.TrayNotification{push("/repositories/notes/commits?ref=main"), push("/repositories/notes/commits?ref=main")}, "/repositories/notes/commits?ref=main"},
		{"pushes", []server.TrayNotification{push("/repositories/notes/commits?ref=main"), push("/repositories/household/commits?ref=main"), push("/activity")}, "/activity"},
		{"pushes and a pull request", []server.TrayNotification{push("/repositories/notes/commits?ref=main"), push("/activity"), pull}, "/"},
		{"a pull request, then pushes", []server.TrayNotification{pull, push("/activity"), push("/activity")}, "/"},
	} {
		var pages clickPages
		for _, notification := range test.shown {
			pages.add(notification)
		}
		if pages.page != test.want {
			t.Errorf("%s: a click opens %q, want %q", test.name, pages.page, test.want)
		}
	}
}
