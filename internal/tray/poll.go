//go:build windows || linux

package tray

import (
	"context"
	"sync/atomic"
	"time"

	"owngit/internal/state"
	"owngit/internal/webui"
)

// How often the icon reads: often while the panel is open or the icon is
// hidden (a file check), less often while the server runs, and least often
// while it does not answer, since the checkup then asks the service manager.
const (
	pollOpen      = 5 * time.Second
	pollHidden    = 5 * time.Second
	pollAnswering = 10 * time.Second
	pollSilent    = 20 * time.Second
)

// reading is one result of the poller.
type reading struct {
	// show is false while the icon is hidden, or while its choice cannot
	// be read (as before the server's first start).
	show   bool
	report Report
}

// poller reads the owner's hidden choice and, while the icon may show, the
// status of the server of stateDir. Each icon draws what it delivers.
type poller struct {
	stateDir  string
	client    *Client
	lang      webui.Lang
	refresh   chan struct{}
	panelOpen atomic.Bool
}

func newPoller(options Options, lang webui.Lang) *poller {
	return &poller{
		stateDir: options.StateDir, client: NewClient(options.StateDir, options.Diagnose),
		lang: lang, refresh: make(chan struct{}, 1),
	}
}

// poll hands a reading to deliver after each read until ctx ends.
func (p *poller) poll(ctx context.Context, deliver func(reading)) {
	for {
		next := reading{show: p.mayShow()}
		wait := pollHidden
		if next.show {
			next.report = p.client.Read(ctx, string(p.lang))
			// The server says so too when the choice changed meanwhile.
			if next.report.Status != nil && !next.report.Status.Shown {
				next.show = false
			}
			switch next.report.Condition {
			case Running, Attention:
				wait = pollAnswering
			default:
				wait = pollSilent
			}
		}
		if p.panelOpen.Load() {
			wait = pollOpen
		}
		// The owner may have hidden the icon while the status was read.
		if next.show && !p.mayShow() {
			next.show = false
		}
		if ctx.Err() != nil {
			return
		}
		deliver(next)
		select {
		case <-ctx.Done():
			return
		case <-p.refresh:
		case <-time.After(wait):
		}
	}
}

// mayShow reports whether the owner lets the icon show: the hidden choice
// can be read and is not set.
func (p *poller) mayShow() bool {
	held, err := state.OpenStateDirectory(p.stateDir)
	if err != nil {
		return false
	}
	defer held.Close()
	hidden, err := state.TrayHidden(held)
	return err == nil && !hidden
}

// askAgain makes the poller read now instead of after its wait.
func (p *poller) askAgain() {
	select {
	case p.refresh <- struct{}{}:
	default:
	}
}
