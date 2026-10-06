package tray

import (
	"testing"
	"time"

	"owngit/internal/server"
)

// The balloon rule: one active balloon, every end of it drains for
// balloonDrain with only a click acting, the next notification waits in a
// one-slot queue and is handed over after the drain, a failure retries on the
// next tick, and the display time starts at SHOW.
func TestBalloonStateOrders(t *testing.T) {
	push := server.TrayNotification{Path: "/repositories/notes/commits"}
	pull := server.TrayNotification{Path: "/repositories/notes/pull-requests/1"}
	check := server.TrayNotification{Path: "/activity"}
	t0 := time.Unix(1000, 0)
	life := 20 * time.Second
	var s balloonState
	handOver := func(n server.TrayNotification, at time.Time) {
		t.Helper()
		s.queue(n)
		if s.phase != balloonIdle {
			t.Fatalf("hand over in phase %v", s.phase)
		}
		s.hand(at)
	}
	drainsFor := func(end func(), at time.Time) {
		t.Helper()
		end()
		if s.phase != balloonDraining || s.due(at.Add(balloonDrain-time.Nanosecond)) || !s.due(at.Add(balloonDrain)) {
			t.Fatalf("an end did not drain the full %v", balloonDrain)
		}
		s.finishDrain()
	}
	if s.due(t0.Add(time.Hour)) || s.click(t0) != "" || s.reported(t0, life) {
		t.Fatal("an idle state acted")
	}

	// Each end path drains: removal, hide or timeout, click, and the no-SHOW bound.
	handOver(push, t0)
	if s.due(t0.Add(balloonNoShow-time.Nanosecond)) || !s.due(t0.Add(balloonNoShow)) {
		t.Fatal("the no-SHOW bound is wrong")
	}
	drainsFor(func() { s.end(t0) }, t0)
	handOver(push, t0)
	s.reported(t0, life)
	if s.due(t0.Add(life-time.Nanosecond)) || !s.due(t0.Add(life)) {
		t.Fatal("the display time did not start at SHOW")
	}
	drainsFor(func() { s.end(t0) }, t0)
	// SHOW 9 s after the hand over (queued behind another application): the
	// display time counts from SHOW.
	handOver(push, t0)
	showAt := t0.Add(9 * time.Second)
	s.reported(showAt, life)
	if s.due(showAt.Add(life-time.Nanosecond)) || !s.due(showAt.Add(life)) {
		t.Fatal("the display time did not start at a delayed SHOW")
	}
	// An early tick 2 ms before the deadline is not due, and the timer is armed
	// for the 2 ms left, so the deadline stays the original one.
	early := showAt.Add(life - 2*time.Millisecond)
	if s.due(early) || s.wait(early) != 2*time.Millisecond {
		t.Fatal("an early tick did not leave 2 ms to the original deadline")
	}
	s.finishDrain()
	handOver(push, t0)
	drainsFor(func() {
		if page := s.click(t0); page != push.Path {
			t.Fatalf("a click opened %q", page)
		}
	}, t0)

	// During the drain duplicate end events and SHOW do nothing, and a click
	// opens the ended balloon's page once. A newer notification replaces the
	// queued one.
	handOver(push, t0)
	s.reported(t0, life)
	s.end(t0)
	end := s.deadline
	s.end(t0.Add(time.Second))
	if s.deadline != end || s.reported(t0.Add(time.Second), life) || s.phase != balloonDraining {
		t.Fatal("a duplicate end or a SHOW changed the drain")
	}
	s.queue(pull)
	s.queue(check)
	if page := s.click(t0.Add(time.Second)); page != push.Path || s.click(t0.Add(time.Second)) != "" || s.deadline != end {
		t.Fatalf("a click while draining opened %q, or did not open once", page)
	}
	s.finishDrain()
	if s.queued == nil || s.queued.Path != check.Path {
		t.Fatal("the newer notification did not replace the queued one")
	}

	// The queued notification is handed over, then SHOW and a click are its own.
	s.hand(t0)
	if !s.reported(t0, life) || s.click(t0) != check.Path {
		t.Fatal("the handed-over balloon's SHOW or click was lost")
	}
	s.finishDrain()

	// A failure keeps the notification queued and retries on the next tick, and
	// only the first failure of a streak asks for a log line.
	s.queue(push)
	if !s.retry(t0) || s.retry(t0) || !s.due(t0.Add(balloonDrain)) || s.queued == nil {
		t.Fatal("a failure did not keep the notification for a retry")
	}
}

// A balloon lives as long as the owner's accessibility display time plus the
// margin, and a fixed time only when that setting cannot be read.
func TestBalloonLifeFollowsTheDisplayTime(t *testing.T) {
	if got := balloonLife(60, true); got != 60*time.Second+balloonMargin {
		t.Fatalf("a 60 s setting gives %v", got)
	}
	if want := balloonFallback + balloonMargin; balloonLife(0, false) != want || balloonLife(0, true) != want {
		t.Fatalf("an unreadable setting gives %v, want %v", balloonLife(0, false), want)
	}
}
