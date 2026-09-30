package state

import (
	"encoding/json"
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
		_, err := store.RecordPush(ctx, pushAt(now.Add(time.Duration(index)*time.Second)))
		noErr(t, err)
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
				_, err := store.RecordPush(ctx, pushAt(now.Add(time.Duration(worker*20+index)*time.Second)))
				errs <- err
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
		if _, err := store.RecordPush(ctx, event); err == nil {
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

// Every start makes a new token in a file private to this account, and
// no temporary file stays behind.
func TestPublishTrayAccess(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	held, err := CreateDirectory(directory)
	noErr(t, err)
	defer held.Close()
	path := filepath.Join(directory, TrayAccessFile)
	read := func() TrayAccess {
		t.Helper()
		content, err := os.ReadFile(path)
		noErr(t, err)
		var access TrayAccess
		noErr(t, json.Unmarshal(content, &access))
		return access
	}
	first, err := PublishTrayAccess(held, "http://127.0.0.1:7654")
	noErr(t, err)
	if len(first.Token) != 43 || len(first.Proof) != 43 || first.Proof == first.Token || first.URL != "http://127.0.0.1:7654" || read() != first {
		t.Fatalf("first access = %+v, file %+v", first, read())
	}
	// A file another account could read gets a private replacement.
	if runtime.GOOS != "windows" {
		noErr(t, os.Chmod(path, 0o644))
	}
	second, err := PublishTrayAccess(held, "http://127.0.0.1:8123")
	noErr(t, err)
	if second.Token == first.Token || len(second.Token) != 43 || read() != second {
		t.Fatalf("second access = %+v after %+v, file %+v", second, first, read())
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		noErr(t, err)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("access file mode %v", info.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(directory)
	noErr(t, err)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "."+TrayAccessFile+"-") {
			t.Fatalf("temporary file %s left behind", entry.Name())
		}
	}
}

// A proof binds the secret, the nonce and the exact body.
func TestTrayProof(t *testing.T) {
	nonce, err := NewTrayNonce()
	noErr(t, err)
	other, err := NewTrayNonce()
	noErr(t, err)
	if !ValidTrayNonce(nonce) || nonce == other || ValidTrayNonce("") || ValidTrayNonce("AAAA") || ValidTrayNonce(nonce+"A") {
		t.Fatalf("nonces %q %q", nonce, other)
	}
	proof := TrayProof("secret", nonce, []byte(`{"ok":true}`))
	for name, changed := range map[string]string{
		"secret": TrayProof("secret2", nonce, []byte(`{"ok":true}`)),
		"nonce":  TrayProof("secret", other, []byte(`{"ok":true}`)),
		"body":   TrayProof("secret", nonce, []byte(`{"ok":true} `)),
	} {
		if changed == proof {
			t.Errorf("another %s gives the same proof", name)
		}
	}
	if proof != TrayProof("secret", nonce, []byte(`{"ok":true}`)) || len(proof) != 43 {
		t.Errorf("proof %q", proof)
	}
}
