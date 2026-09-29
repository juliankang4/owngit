package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func pushAt(at time.Time) PushEvent {
	return PushEvent{RepositoryID: "project", Ref: "refs/heads/main", OldOID: strings.Repeat("a", 40), NewOID: strings.Repeat("b", 40),
		RefsUpdated: 1, Actor: Actor{Kind: ActorAccess}, PushedAt: at}
}

// The newest PushEventsKept pushes stay: the 101st removes the oldest.
func TestRecordPushKeepsTheNewestHundred(t *testing.T) {
	store, ctx, now := newProjectStore(t)
	for index := range PushEventsKept + 1 {
		noErr(t, store.RecordPush(ctx, pushAt(now.Add(time.Duration(index)*time.Second))))
	}
	events, err := store.RecentPushes(ctx, 1000)
	noErr(t, err)
	if len(events) != PushEventsKept {
		t.Fatalf("kept %d push events, want %d", len(events), PushEventsKept)
	}
	if newest, oldest := events[0].PushedAt, events[len(events)-1].PushedAt; !newest.Equal(now.Add(PushEventsKept*time.Second)) || !oldest.Equal(now.Add(time.Second)) {
		t.Fatalf("kept pushes from %v to %v", oldest, newest)
	}
	if first := events[0]; first.RepositoryName != "Project" || first.Actor != (Actor{Kind: ActorAccess}) || first.RefsUpdated != 1 {
		t.Fatalf("newest push = %+v", first)
	}
	latest, err := store.RecentPushes(ctx, 3)
	noErr(t, err)
	if len(latest) != 3 || !latest[2].PushedAt.Equal(now.Add(98*time.Second)) {
		t.Fatalf("latest three = %+v", latest)
	}
}

// Pushes recorded at the same time still leave exactly the newest
// PushEventsKept.
func TestConcurrentPushesKeepTheBound(t *testing.T) {
	store, ctx, now := newProjectStore(t)
	var group sync.WaitGroup
	errs := make(chan error, 160)
	for worker := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range 20 {
				errs <- store.RecordPush(ctx, pushAt(now.Add(time.Duration(worker*20+index)*time.Second)))
			}
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		noErr(t, err)
	}
	var count, lowest, highest int
	noErr(t, store.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(sequence), MAX(sequence) FROM push_events`).Scan(&count, &lowest, &highest))
	if count != PushEventsKept || highest != 160 || highest-lowest != PushEventsKept-1 {
		t.Fatalf("kept %d push events, sequences %d to %d", count, lowest, highest)
	}
}

func TestRecordPushRefusesAnIncompleteEvent(t *testing.T) {
	store, ctx, now := newProjectStore(t)
	for name, change := range map[string]func(*PushEvent){
		"no refs":      func(event *PushEvent) { event.RefsUpdated = 0 },
		"no ref":       func(event *PushEvent) { event.Ref = "" },
		"no time":      func(event *PushEvent) { event.PushedAt = time.Time{} },
		"bad object":   func(event *PushEvent) { event.NewOID = "main" },
		"bad actor":    func(event *PushEvent) { event.Actor = Actor{Kind: "someone"} },
		"unknown repo": func(event *PushEvent) { event.RepositoryID = "missing" },
	} {
		event := pushAt(now)
		change(&event)
		if err := store.RecordPush(ctx, event); err == nil {
			t.Errorf("%s: recorded", name)
		}
	}
	if events, err := store.RecentPushes(ctx, 10); err != nil || len(events) != 0 {
		t.Fatalf("refused events left %d rows, err=%v", len(events), err)
	}
}

func TestTrayHiddenPersists(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	held, err := CreateDirectory(directory)
	noErr(t, err)
	defer held.Close()
	if hidden, err := TrayHidden(held); err != nil || hidden {
		t.Fatalf("new state: hidden=%v err=%v", hidden, err)
	}
	noErr(t, SetTrayHidden(held, true))
	noErr(t, SetTrayHidden(held, true))
	again, err := OpenStateDirectory(directory)
	noErr(t, err)
	defer again.Close()
	if hidden, err := TrayHidden(again); err != nil || !hidden {
		t.Fatalf("after hiding: hidden=%v err=%v", hidden, err)
	}
	noErr(t, SetTrayHidden(again, false))
	noErr(t, SetTrayHidden(again, false))
	if hidden, err := TrayHidden(held); err != nil || hidden {
		t.Fatalf("after showing: hidden=%v err=%v", hidden, err)
	}
}

// The token survives a restart and an address change, is private to this
// account, and a damaged file is replaced with a new token.
func TestPublishTrayAccess(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	held, err := CreateDirectory(directory)
	noErr(t, err)
	defer held.Close()
	first, err := PublishTrayAccess(held, "http://127.0.0.1:7654")
	noErr(t, err)
	if len(first.Token) != 43 || first.URL != "http://127.0.0.1:7654" {
		t.Fatalf("first access = %+v", first)
	}
	path := filepath.Join(directory, TrayAccessFile)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		noErr(t, err)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("access file mode %v", info.Mode().Perm())
		}
	}
	same, err := PublishTrayAccess(held, "http://127.0.0.1:7654")
	noErr(t, err)
	moved, err := PublishTrayAccess(held, "http://127.0.0.1:8123")
	noErr(t, err)
	if same != first || moved.Token != first.Token || moved.URL != "http://127.0.0.1:8123" {
		t.Fatalf("same=%+v moved=%+v first=%+v", same, moved, first)
	}
	if read, err := readTrayAccess(held); err != nil || read != moved {
		t.Fatalf("file holds %+v err=%v", read, err)
	}
	noErr(t, os.WriteFile(path, []byte("{\"url\":\"http://127.0.0.1:8123\",\"token\":\"short\"}"), 0o600))
	replaced, err := PublishTrayAccess(held, "http://127.0.0.1:8123")
	noErr(t, err)
	if replaced.Token == first.Token || len(replaced.Token) != 43 {
		t.Fatalf("damaged file kept token %+v", replaced)
	}
	entries, err := os.ReadDir(directory)
	noErr(t, err)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tray-access-") {
			t.Fatalf("temporary file %s left behind", entry.Name())
		}
	}
}
