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

// clickPages is the page that a click on any of the notifications shown
// so far opens, where Windows does not say which of them was clicked: the
// page of them all when they share one, the activity page when they are
// all pushes, and otherwise the dashboard's home page.
type clickPages struct {
	page       string
	pushesOnly bool
}

func (pages *clickPages) add(notification server.TrayNotification) {
	push := notification.Kind == state.NotifyPush
	switch {
	case pages.page == "":
		pages.page, pages.pushesOnly = notification.Path, push
	case pages.page == notification.Path:
	case pages.pushesOnly && push:
		pages.page = "/activity"
	default:
		pages.page, pages.pushesOnly = "/", false
	}
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

// NotificationPanel is the notification settings of this computer as the
// panel shows them.
type NotificationPanel struct {
	Heading string `json:"heading"`
	Hint    string `json:"hint"`
	// Error says why the settings could not be read, or ""; Settings is
	// then empty.
	Error    string                `json:"error"`
	Settings []NotificationSetting `json:"settings"`
}

// NotificationSetting is one switch: "all", "only_others" or a kind.
type NotificationSetting struct {
	Setting string `json:"setting"`
	Label   string `json:"label"`
	On      bool   `json:"on"`
	// Enabled is false for the other switches while all notifications
	// are off; they keep their own choice.
	Enabled bool `json:"enabled"`
}

var settingLabels = map[string]webui.MessageCode{
	"all": webui.MsgNotifySettingAll, "only_others": webui.MsgNotifySettingOthers,
	state.NotifyPush: webui.MsgNotifySettingPush, state.NotifyPullRequest: webui.MsgNotifySettingPR,
	state.NotifyCheckFailed: webui.MsgNotifySettingCheck, state.NotifyImportFailed: webui.MsgNotifySettingImport,
	state.NotifyBackupFailed: webui.MsgNotifySettingBackup, state.NotifyUpdate: webui.MsgNotifySettingUpdate,
}

// readNotificationPanel reads the notification settings in stateDir for
// the panel, in lang.
func readNotificationPanel(stateDir string, lang webui.Lang) NotificationPanel {
	panel := NotificationPanel{
		Heading: webui.Text(lang, webui.MsgNotifySettings), Hint: webui.Text(lang, webui.MsgNotifySettingsHint),
		Settings: []NotificationSetting{},
	}
	choice, err := readNotifications(stateDir)
	if err != nil {
		panel.Error = fmt.Sprintf(webui.Text(lang, webui.MsgNotifySettingsFailed), err)
		return panel
	}
	add := func(setting string, on, enabled bool) {
		panel.Settings = append(panel.Settings, NotificationSetting{Setting: setting, Label: webui.Text(lang, settingLabels[setting]), On: on, Enabled: enabled})
	}
	add("all", !choice.Off, true)
	add("only_others", choice.OnlyOthers, !choice.Off)
	for _, kind := range state.NotifyKinds {
		add(kind, !slices.Contains(choice.KindsOff, kind), !choice.Off)
	}
	return panel
}

func readNotifications(stateDir string) (state.TrayNotifications, error) {
	held, err := state.OpenStateDirectory(stateDir)
	if err != nil {
		return state.TrayNotifications{}, err
	}
	defer held.Close()
	return state.ReadTrayNotifications(held)
}

// setNotification turns one notification setting of stateDir on or off.
func setNotification(stateDir, setting string, on bool) error {
	held, err := state.OpenStateDirectory(stateDir)
	if err != nil {
		return err
	}
	defer held.Close()
	choice, err := state.ReadTrayNotifications(held)
	if err != nil {
		return err
	}
	if choice, err = choice.With(setting, on); err != nil {
		return err
	}
	return state.WriteTrayNotifications(held, choice)
}
