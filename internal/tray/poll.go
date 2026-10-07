//go:build windows || linux

package tray

import (
	"context"
	"log"
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
	// show is false only for a missing state directory or the owner's
	// hidden choice. Read errors stay visible.
	show   bool
	report Report
	// unavailable is the sentence that says this desktop cannot show
	// notifications, or "" while it shows them. The poller reads it where it
	// asked the desktop, so the notifier's state needs no sharing.
	unavailable string
}

// poller reads the owner's hidden choice and, while the icon may show, the
// status of the server of stateDir. Each icon draws what it delivers.
type poller struct {
	stateDir  string
	client    *Client
	lang      webui.Lang
	refresh   chan struct{}
	panelOpen atomic.Bool
	// notifier shows desktop notifications after each reading that shows
	// a server that answered, once the icon sets its show.
	notifier notifier
}

func newPoller(options Options, lang webui.Lang) *poller {
	client := NewClient(options.StateDir, options.Diagnose)
	return &poller{
		stateDir: options.StateDir, client: client, lang: lang, refresh: make(chan struct{}, 1),
		notifier: notifier{stateDir: options.StateDir, client: client, lang: lang},
	}
}

// poll hands a reading to deliver after each read until ctx ends.
func (p *poller) poll(ctx context.Context, deliver func(reading)) {
	for {
		show, showErr := Shown(p.stateDir)
		next := reading{show: show, unavailable: p.notifier.unavailable(p.lang)}
		wait := pollHidden
		if next.show {
			if showErr != nil {
				next.report = p.client.unavailable(showErr, string(p.lang))
			} else {
				next.report = p.client.Read(ctx, string(p.lang))
			}
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
		if next.show {
			show, err := Shown(p.stateDir)
			next.show = show
			if err != nil {
				next.report = p.client.unavailable(err, string(p.lang))
			}
		}
		if ctx.Err() != nil {
			return
		}
		deliver(next)
		switch {
		case next.show && next.report.Status != nil && p.notifier.show != nil:
			p.notifier.notify(ctx, time.Now())
		case !next.show:
			p.forgetCursor()
		}
		select {
		case <-ctx.Done():
			return
		case <-p.refresh:
		case <-time.After(wait):
		}
	}
}

// forgetCursor removes the notification cursor while the owner hides the
// icon, as hiding does, so that a notification read that ended just after
// the owner hid the icon cannot keep a cursor: shown again, the icon shows
// only what happens from then on.
func (p *poller) forgetCursor() {
	held, err := state.OpenStateDirectory(p.stateDir)
	if err != nil {
		return
	}
	defer held.Close()
	if hidden, err := state.TrayHidden(held); err == nil && hidden {
		if err := state.RemoveTrayCursor(held); err != nil {
			log.Printf("OwnGit icon notifications: %v", err)
		}
	}
}

// askAgain makes the poller read now instead of after its wait.
func (p *poller) askAgain() {
	select {
	case p.refresh <- struct{}{}:
	default:
	}
}
