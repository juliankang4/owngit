package tray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"slices"
	"strings"

	"owngit/internal/server"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// Events reads the event feed after cursor, "" for the first read, with
// the kinds and the "only what I did not do" choice of choice, in lang.
func (client *Client) Events(ctx context.Context, lang, cursor string, choice state.TrayNotifications) (server.TrayEvents, error) {
	query := url.Values{"lang": {lang}, "kinds": {strings.Join(choice.Kinds(), ",")}, "only_others": {"0"}}
	if choice.OnlyOthers {
		query.Set("only_others", "1")
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	var events server.TrayEvents
	_, err := client.get(ctx, server.TrayEventsPath, query, func(body []byte) error {
		if err := json.Unmarshal(body, &events); err != nil {
			return fmt.Errorf("read the event feed: %w", err)
		}
		if !events.OK || !state.ValidTrayCursor(events.Cursor) {
			return errors.New("the answer is not OwnGit's event feed")
		}
		for _, notification := range events.Notifications {
			if notification.ID == "" || !slices.Contains(state.NotifyKinds, notification.Kind) || !dashboardPath(notification.Path) {
				return errors.New("the event feed holds a notification that is not OwnGit's")
			}
		}
		return nil
	})
	return events, err
}

// dashboardPath reports whether path is a page of the dashboard: it begins
// with one "/", so the address stays on the dashboard's server.
func dashboardPath(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") && !strings.ContainsAny(path, "\\\x00\r\n")
}

// DashboardPage returns the address of path on the dashboard, only when
// the server proves right now that it answers there, as Dashboard does.
func (client *Client) DashboardPage(ctx context.Context, lang, path string) (string, error) {
	if !dashboardPath(path) {
		return "", fmt.Errorf("%q is not a dashboard page", path)
	}
	dashboard, err := client.Dashboard(ctx, lang)
	if err != nil {
		return "", err
	}
	return dashboard + path, nil
}

// notifier shows the notifications of the event feed and keeps the feed's
// cursor in the state directory once they are shown. The icon runs it after
// each reading in which the server answered and the icon shows.
type notifier struct {
	stateDir string
	client   *Client
	lang     webui.Lang
	// show shows one notification. An error leaves it and the rest for the
	// next read.
	show func(server.TrayNotification) error
	// shown are the notifications shown since the cursor last moved, so a
	// read that could not keep its cursor does not show them again.
	shown map[string]bool
}

// notify reads the feed once and shows what it reports. Nothing shows once
// the owner hid the icon, and then the cursor stays as the hiding left it.
func (n *notifier) notify(ctx context.Context) {
	held, err := state.OpenStateDirectory(n.stateDir)
	if err != nil {
		log.Printf("OwnGit icon notifications: %v", err)
		return
	}
	defer held.Close()
	choice, err := state.ReadTrayNotifications(held)
	if err != nil {
		log.Printf("OwnGit icon notifications: %v", err)
		return
	}
	cursor, err := state.ReadTrayCursor(held)
	if err != nil {
		log.Printf("OwnGit icon notifications start again from now: %v", err)
		cursor = ""
	}
	events, err := n.client.Events(ctx, string(n.lang), cursor, choice)
	if err != nil {
		log.Printf("OwnGit icon notifications: %v", err)
		return
	}
	if n.shown == nil {
		n.shown = map[string]bool{}
	}
	for _, notification := range events.Notifications {
		if n.shown[notification.ID] {
			continue
		}
		if hidden(held) {
			return
		}
		if err := n.show(notification); err != nil {
			log.Printf("OwnGit icon notifications: %v", err)
			return
		}
		n.shown[notification.ID] = true
	}
	if hidden(held) {
		return
	}
	if err := state.WriteTrayCursor(held, events.Cursor); err != nil {
		log.Printf("OwnGit icon notifications: %v", err)
		return
	}
	clear(n.shown)
}

// hidden reports whether the owner hid the icon, or whether that cannot be
// read, which leaves the icon hidden too.
func hidden(held *os.File) bool {
	hidden, err := state.TrayHidden(held)
	return err != nil || hidden
}
