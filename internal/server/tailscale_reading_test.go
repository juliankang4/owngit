package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
	"owngit/internal/webui"
)

// A reading after a change is a new one: a report does not join the reading
// that started before the change (forget), which may show Tailscale as it
// was, and the new reading runs after that one, so Tailscale's commands
// never overlap.
func TestReadingAfterAChangeIsNewAndRunsAfterTheEarlierOne(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	sharing := app.Tailscale
	fake.Update(func(s *tailscaletest.State) { s.ReadDelay, s.Calls, s.MaxRunning = 1000, nil, 0 })
	reads := func() int {
		count := 0
		for _, call := range fake.Calls() {
			if call == tailscaletest.StatusRead || call == tailscaletest.ServeRead {
				count++
			}
		}
		return count
	}
	ctx := context.Background()
	first := make(chan tailscaleReading, 1)
	go func() {
		reading, _ := sharing.read(ctx)
		first <- reading
	}()
	// The first reading has read the status; its Serve configuration is
	// being read now. Then Tailscale changes, and OwnGit knows it.
	for deadline := time.Now().Add(30 * time.Second); reads() == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the first reading did not start")
		}
	}
	const marker = "marker.example:9999"
	fake.Update(func(s *tailscaletest.State) {
		s.Serve.Web = map[string]tailscale.WebServer{marker: {Handlers: map[string]tailscale.Handler{"/": {Proxy: "http://127.0.0.1:1"}}}}
	})
	sharing.forget()

	second, err := sharing.read(ctx)
	noErr(t, err)
	if n := reads(); n != 4 {
		t.Fatalf("the report after the change got the earlier reading: %d reads, want 4 (%q)", n, fake.Calls())
	}
	if _, ok := second.config.Web[marker]; !ok {
		t.Fatalf("the reading after the change does not show it: %v", second.config.Web)
	}
	<-first
	if most := fake.State().MaxRunning; most != 1 {
		t.Fatalf("%d Tailscale commands ran at once, want 1 (%q)", most, fake.Calls())
	}
}

// Stopping the server ends a background reading that Tailscale does not
// answer: its command is stopped, Stop waits until the reading returned, so
// no tailscale process is left, and later reports start no command.
func TestStoppingEndsTheBackgroundReading(t *testing.T) {
	app, fake := tailscaleApp(t, tailscaletest.State{Status: tailscaletest.Running()})
	sharing := app.Tailscale
	fake.Update(func(s *tailscaletest.State) { s.ReadDelay, s.Calls, s.Running, s.MaxRunning = 60000, nil, 0, 0 })
	reading := make(chan tailscaleReading, 1)
	go func() {
		result, _ := sharing.read(context.Background())
		reading <- result
	}()
	for deadline := time.Now().Add(30 * time.Second); fake.State().Running == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the reading did not start its command")
		}
	}
	stopContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := sharing.Stop(stopContext); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// Stop returned, so the reading returned, and its command was waited
	// for: it ended before answering.
	if result := <-reading; tailscale.KindOf(result.statusErr) != tailscale.KindTimeout || len(fake.Calls()) != 0 {
		t.Fatalf("the reading after stop: status error %v, calls %q", result.statusErr, fake.Calls())
	}
	if _, err := sharing.read(context.Background()); !errors.Is(err, errReadingsStopped) {
		t.Fatalf("a report after stop: %v", err)
	}
	if running := fake.State().Running; running != 1 || len(fake.Calls()) != 0 {
		t.Fatalf("a report after stop started a command: running %d, calls %q", running, fake.Calls())
	}
	// The Tailscale block then says only that this is unavailable, and
	// nothing is logged, since nothing failed.
	serverLog := captureServerLog(t)
	if info := app.tailscaleBlock(httptest.NewRequest(http.MethodGet, "/settings", nil), true, "", nil); info.Problem != webui.MsgErrUnavailable {
		t.Fatalf("the block after stop shows %q, want %q", info.Problem, webui.MsgErrUnavailable)
	}
	checkLoggedSteps(t, "the block after stop", loggedFailures(serverLog, 0))
}
