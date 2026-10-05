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
	"time"

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

// clickPages is the page that a click on the balloon Windows shows opens.
// Windows says only that one of the icon's balloons was clicked, not which one,
// and it shows one balloon at a time: a new balloon replaces the one on screen,
// so the page kept is the one of the balloon handed over last. A click after
// that balloon is gone opens nothing, never the page of an earlier
// notification, and never one page for everything shown since the icon started.
type clickPages struct {
	page string
}

// add records the balloon handed to Windows for notification, which replaces
// the balloon on screen and its page.
func (pages *clickPages) add(notification server.TrayNotification) {
	pages.page = notification.Path
}

// clicked returns the page of the balloon Windows reports clicked and records
// that balloon gone: Windows sends the click instead of a hide for it, so a
// later click opens nothing rather than a page the owner has already seen.
func (pages *clickPages) clicked() string {
	page := pages.page
	pages.page = ""
	return page
}

// gone records that Windows reported the balloon on screen gone without a
// click: it was dismissed or it timed out.
func (pages *clickPages) gone() {
	pages.page = ""
}

// summaryLimit is how many notifications of one read the icon hands to a
// desktop that shows one of them at a time: a longer read becomes one balloon
// that counts them, so a backlog is not trickled out balloon by balloon.
const summaryLimit = 3

// balloonFor is the one balloon that stands for the notifications of one read
// and how many of them it stands for: the first notification while the read
// holds at most summaryLimit of them, and otherwise one summary balloon that
// counts them all.
func balloonFor(notifications []server.TrayNotification, lang webui.Lang) (server.TrayNotification, int) {
	if len(notifications) > summaryLimit {
		return summaryBalloon(notifications, lang), len(notifications)
	}
	return notifications[0], 1
}

// summaryBalloon is the one balloon that stands for the notifications of one
// read and names how many there are. There is no page they all share, so a
// click on it opens the dashboard's home page.
func summaryBalloon(notifications []server.TrayNotification, lang webui.Lang) server.TrayNotification {
	return server.TrayNotification{
		Title: fmt.Sprintf(webui.Text(lang, webui.MsgNotifyMany), len(notifications)),
		Body:  webui.Text(lang, webui.MsgNotifyManyHint),
		Path:  "/",
	}
}

// balloonText is the title and the body of the notification area balloon that
// shows notification. The body is never empty: a balloon with an empty body
// removes the notification instead of showing it, and the icon cannot tell,
// because the notification area reports that it took the balloon either way. A
// notification without a body therefore shows its title as the body under
// OwnGit's name. NULs are removed because the balloon text is UTF-16.
func balloonText(notification server.TrayNotification) (title, body string) {
	plain := func(text string) string { return strings.ReplaceAll(text, "\x00", "") }
	title, body = plain(notification.Title), plain(notification.Body)
	if subtitle := plain(notification.Subtitle); subtitle != "" {
		body = subtitle + "\n" + body
	}
	if body == "" {
		return "OwnGit", title
	}
	return title, body
}

// errNoNotificationService means this computer has no notification service, so
// nothing can be shown until one starts.
var errNoNotificationService = errors.New("this desktop has no notification service")

// notifier shows the notifications of the event feed and keeps the feed's
// cursor in the state directory once they are shown. The icon runs it after
// each reading in which the server answered and the icon shows.
type notifier struct {
	stateDir string
	client   *Client
	lang     webui.Lang
	// show hands the notifications of one read to the platform, in order, and
	// reports how many of them, from the first, the platform took for display.
	// The rest stay for the next read. An error means the platform took none
	// after the ones it reports.
	show func(notifications []server.TrayNotification) (int, error)
	// shown are the notifications shown since the cursor last moved, so a read
	// that could not keep its cursor does not show them again.
	shown map[string]bool
	// failure is the reason showings failed in a row, or nil once one is shown
	// again, and failures counts them. retryAt is when the platform may be
	// asked again, so a desktop that cannot show notifications is asked rarely
	// instead of on every read.
	failure  error
	failures int
	retryAt  time.Time
}

// notifyRetryFirst and notifyRetryLongest bound the wait before the icon asks
// the platform to show notifications again after a failure: the wait doubles
// each time up to the longest.
const (
	notifyRetryFirst   = 10 * time.Second
	notifyRetryLongest = 5 * time.Minute
)

// retryWait is how long the icon waits before asking the platform again, after
// failures showings in a row that failed.
func retryWait(failures int) time.Duration {
	return min(notifyRetryFirst<<min(failures-1, 5), notifyRetryLongest)
}

// notify reads the feed once and shows what it reports, no sooner than the wait
// after a failure. Nothing shows once the owner hid the icon, and then the
// cursor stays as the hiding left it. The cursor moves only over the
// notifications the platform took.
func (n *notifier) notify(ctx context.Context, now time.Time) {
	if now.Before(n.retryAt) {
		return
	}
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
	pending := []server.TrayNotification{}
	for _, notification := range events.Notifications {
		if !n.shown[notification.ID] {
			pending = append(pending, notification)
		}
	}
	if len(pending) > 0 && !hidden(held) {
		taken, err := n.show(pending)
		taken = min(max(taken, 0), len(pending))
		for _, notification := range pending[:taken] {
			n.shown[notification.ID] = true
		}
		if taken > 0 {
			// The desktop showed a notification, so it can show them: the panel
			// stops saying otherwise and the next read is not delayed.
			n.failure, n.failures, n.retryAt = nil, 0, time.Time{}
		}
		switch {
		case err != nil:
			// The platform did not take the rest of them.
			n.failed(now, err)
			return
		case taken < len(pending):
			// The platform shows one at a time: the rest wait for the next
			// read, and the cursor waits with them.
			return
		}
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

// failed records that showing failed: the reason is written once for the
// failures in a row, and the wait before the next attempt is longer than the
// one before, up to notifyRetryLongest.
func (n *notifier) failed(now time.Time, err error) {
	if n.failures == 0 {
		log.Printf("OwnGit icon notifications: %v", err)
	}
	n.failure = err
	n.failures++
	n.retryAt = now.Add(retryWait(n.failures))
}

// unavailable is the sentence that says this desktop cannot show notifications,
// or "" while it shows them.
func (n *notifier) unavailable(lang webui.Lang) string {
	if !errors.Is(n.failure, errNoNotificationService) {
		return ""
	}
	return webui.Text(lang, webui.MsgNotifyUnavailable)
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
	Error string `json:"error"`
	// Unavailable says that this desktop cannot show notifications, in the
	// owner's words, or "" while it shows them.
	Unavailable string                `json:"unavailable"`
	Settings    []NotificationSetting `json:"settings"`
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
