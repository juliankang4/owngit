package server

import (
	"context"
	"testing"
	"time"

	"owngit/internal/tailscale"
	"owngit/internal/tailscale/tailscaletest"
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
			if call == "status --json" || call == "serve status --json" {
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
