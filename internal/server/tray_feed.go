package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"owngit/internal/logtext"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// TrayEventsPath is the event feed the tray icon of this computer reads to
// show desktop notifications. It answers under the same rules as
// TrayStatusPath: only a direct request from this computer with the token
// of state.TrayAccessFile, and every answer proves the request's nonce.
//
// The icon sends the cursor of its last answer, the kinds it shows
// ("kinds", comma separated, state.NotifyKinds) and whether it drops what
// came from this computer ("only_others=1"). The answer holds the
// notifications to show, already grouped and worded in the language asked
// for, and the cursor to send next, which the icon keeps once it has shown
// them. A kind that is off is dropped and never reported later.
const TrayEventsPath = "/tray/events"

// trayFeedDelay is how old a record is before the feed reports it. A record
// written slightly later than its own time is then still read, and pushes
// within this long of the first one are reported together.
const trayFeedDelay = time.Minute

// trayFeedSeparate is how many records of one kind a read reports one by
// one; more become one notification that counts them.
const trayFeedSeparate = 3

// trayRecordKinds are the kinds the feed reads by the time their records
// were written.
var trayRecordKinds = []string{state.NotifyPullRequest, state.NotifyCheckFailed, state.NotifyImportFailed, state.NotifyBackupFailed}

// trayBoundaryRecords bounds the records of one kind a cursor remembers
// as already there in the second it starts at. More in that one second
// leave the whole second as before the start.
const trayBoundaryRecords = 3

// TrayEvents is the answer of TrayEventsPath.
type TrayEvents struct {
	OK bool `json:"ok"`
	// Cursor is what the icon sends next, once it has shown Notifications.
	Cursor string `json:"cursor"`
	// Started is true when the feed started over from now: the request had
	// no cursor, or its cursor belongs to records this server no longer
	// has, such as after a restore. Nothing earlier is reported then.
	Started       bool               `json:"started"`
	Notifications []TrayNotification `json:"notifications"`
}

// TrayNotification is one desktop notification. Every string is plain text.
type TrayNotification struct {
	// ID names the notification, so it is shown once.
	ID string `json:"id"`
	// Kind is one of state.NotifyKinds.
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Body     string `json:"body"`
	// Path is the dashboard page that clicking opens, beginning with "/".
	Path string `json:"path"`
}

// trayCursor is what the feed reported. It travels as unpadded base64url
// JSON, which the icon keeps without reading.
type trayCursor struct {
	// Push is the sequence of the last push reported or dropped.
	Push int64 `json:"p"`
	// Times holds, for each of trayRecordKinds, the time in Unix seconds up
	// to which its records were reported or dropped.
	Times map[string]int64 `json:"t"`
	// Seen holds, for a kind whose time was set to the present (a new
	// cursor, or the kind off), the marks of its records already written
	// in that second. The records of that second that are not among them
	// came later and are still reported. Records store whole seconds, so
	// the second alone cannot tell them apart.
	Seen map[string][]string `json:"s,omitempty"`
	// Update is the newer release reported or known when the feed started.
	Update string `json:"u"`
}

func (cursor trayCursor) encode() string {
	content, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(content)
}

func decodeTrayCursor(value string) (trayCursor, bool) {
	content, err := base64.RawURLEncoding.DecodeString(value)
	var cursor trayCursor
	if err != nil || json.Unmarshal(content, &cursor) != nil || cursor.Push < 0 {
		return trayCursor{}, false
	}
	for _, kind := range trayRecordKinds {
		if cursor.Times[kind] <= 0 || len(cursor.Seen[kind]) > trayBoundaryRecords {
			return trayCursor{}, false
		}
	}
	return cursor, true
}

// recordMark is how a cursor names a record it saw: short, since the
// cursor is kept in a small file, and enough to tell the few records of
// one second apart.
func recordMark(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:3])
}

// startKind sets the time of kind to now and remembers the records of kind
// already written in that second.
func (app *App) startKind(ctx context.Context, cursor *trayCursor, kind string, now time.Time) error {
	// Check jobs keep nanoseconds, so a record of this second can be later
	// than now itself.
	inSecond := func(record state.FeedRecord) bool { return record.At.Equal(now) }
	records, total, err := app.Store.FeedRecords(ctx, kind, now.Add(-time.Second), now.Add(time.Second), inSecond, trayBoundaryRecords)
	if err != nil {
		return err
	}
	cursor.Times[kind] = now.Unix()
	if total > trayBoundaryRecords {
		delete(cursor.Seen, kind)
		return nil
	}
	seen := []string{}
	for _, record := range records {
		seen = append(seen, recordMark(record.ID))
	}
	cursor.Seen[kind] = seen
	return nil
}

// trayOrigins remembers, in this process only, which records came from a
// request from this computer, by the rule of fromThisComputer. A record
// that was written before this process started, or that the bound pushed
// out, counts as not from this computer, so it is shown, never dropped.
type trayOrigins struct {
	mu    sync.Mutex
	keys  map[string]bool
	order []string
}

// trayOriginsKept bounds the records trayOrigins remembers.
const trayOriginsKept = 1000

// note remembers that the record key came from request, when it came from
// this computer.
func (origins *trayOrigins) note(request *http.Request, key string) {
	if !fromThisComputer(request) {
		return
	}
	origins.mu.Lock()
	defer origins.mu.Unlock()
	if origins.keys == nil {
		origins.keys = map[string]bool{}
	}
	if origins.keys[key] {
		return
	}
	if len(origins.order) == trayOriginsKept {
		delete(origins.keys, origins.order[0])
		origins.order = origins.order[1:]
	}
	origins.keys[key] = true
	origins.order = append(origins.order, key)
}

func (origins *trayOrigins) fromThisComputer(key string) bool {
	origins.mu.Lock()
	defer origins.mu.Unlock()
	return origins.keys[key]
}

// originKey is the key of a record in trayOrigins: its kind and its ID,
// which for a push is its sequence and for the other kinds the ID of its
// state.FeedRecord.
func originKey(kind, id string) string { return kind + ":" + id }

func pushOrigin(sequence int64) string {
	return originKey(state.NotifyPush, strconv.FormatInt(sequence, 10))
}

// pullRequestID is the ID of a pull request's state.FeedRecord.
func pullRequestID(repositoryID string, number int64) string {
	return repositoryID + "/" + strconv.FormatInt(number, 10)
}

// noteImportOrigin remembers where the import run that request started
// came from. A run that did not start has no ID and nothing to remember.
func (app *App) noteImportOrigin(request *http.Request, run state.ImportRun) {
	if run.ID != "" {
		app.trayOrigins.note(request, originKey(state.NotifyImportFailed, run.ID))
	}
}

// trayFeedRequest is what the icon asked for.
type trayFeedRequest struct {
	lang       webui.Lang
	kinds      []string
	onlyOthers bool
	// cursor is valid only when resume is true.
	cursor trayCursor
	resume bool
}

func (feed trayFeedRequest) shows(kind string) bool { return slices.Contains(feed.kinds, kind) }

// dropped reports whether the record key is left out because it came from
// this computer and the icon drops those.
func (app *App) dropped(feed trayFeedRequest, key string) bool {
	return feed.onlyOthers && app.trayOrigins.fromThisComputer(key)
}

func parseTrayFeedRequest(query url.Values) (trayFeedRequest, error) {
	lang, _ := webui.ParseLang(query.Get("lang"))
	feed := trayFeedRequest{lang: lang, kinds: []string{}}
	if kinds := query.Get("kinds"); kinds != "" {
		for _, kind := range strings.Split(kinds, ",") {
			if !slices.Contains(state.NotifyKinds, kind) {
				return trayFeedRequest{}, fmt.Errorf("kinds names the unknown kind %q", kind)
			}
			feed.kinds = append(feed.kinds, kind)
		}
	}
	switch query.Get("only_others") {
	case "", "0":
	case "1":
		feed.onlyOthers = true
	default:
		return trayFeedRequest{}, fmt.Errorf("only_others is 1 or 0")
	}
	if value := query.Get("cursor"); value != "" {
		if !state.ValidTrayCursor(value) {
			return trayFeedRequest{}, fmt.Errorf("cursor is not a cursor of this feed")
		}
		feed.cursor, feed.resume = decodeTrayCursor(value)
	}
	return feed, nil
}

func (app *App) handleTrayEvents(writer http.ResponseWriter, request *http.Request) {
	nonce, ok := app.trayRequest(writer, request)
	if !ok {
		return
	}
	feed, err := parseTrayFeedRequest(request.URL.Query())
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request", "The event feed request is not valid: "+err.Error()+".", nil)
		return
	}
	events, err := app.trayEvents(request, feed)
	if err != nil {
		writeAPIError(writer, unavailable(request, "tray event feed read", err), "events_unavailable", "OwnGit runs but could not read its events. The OwnGit log says why.", nil)
		return
	}
	app.writeTrayAnswer(writer, nonce, events)
}

// trayEvents reads what happened since feed's cursor.
func (app *App) trayEvents(request *http.Request, feed trayFeedRequest) (TrayEvents, error) {
	ctx := request.Context()
	lastPush, err := app.Store.LastPushSequence(ctx)
	if err != nil {
		return TrayEvents{}, err
	}
	settings, err := app.Store.Settings(ctx)
	if err != nil {
		return TrayEvents{}, err
	}
	now := app.now().Truncate(time.Second)
	until := now.Add(-trayFeedDelay)
	update := ""
	if app.Releases != nil && settings.UpdateCheck {
		if release, newer := app.Releases.Newer(); newer {
			update = release.Version
		}
	}
	cursor := feed.cursor
	// A new cursor starts now: what came before is never reported. A
	// cursor past the last push this database ever recorded belongs to
	// other records, such as those a restore replaced.
	if !feed.resume || cursor.Push > lastPush {
		cursor = trayCursor{Push: lastPush, Times: map[string]int64{}, Seen: map[string][]string{}, Update: update}
		for _, kind := range trayRecordKinds {
			if err := app.startKind(ctx, &cursor, kind, now); err != nil {
				return TrayEvents{}, err
			}
		}
		return TrayEvents{OK: true, Cursor: cursor.encode(), Started: true, Notifications: []TrayNotification{}}, nil
	}
	events := TrayEvents{OK: true, Notifications: []TrayNotification{}}
	pushes, err := app.Store.PushesAfter(ctx, cursor.Push)
	if err != nil {
		return TrayEvents{}, err
	}
	var notifications []TrayNotification
	notifications, cursor.Push = app.pushNotifications(request, feed, pushes, cursor.Push, lastPush)
	events.Notifications = append(events.Notifications, notifications...)
	if cursor.Seen == nil {
		cursor.Seen = map[string][]string{}
	}
	for _, kind := range trayRecordKinds {
		// A kind that is off drops what was written up to now; one that is
		// on reads what is a minute old. A clock that went back leaves the
		// time where it was until it passes it again.
		after := time.Unix(cursor.Times[kind], 0)
		if !feed.shows(kind) {
			if !now.Before(after) {
				if err := app.startKind(ctx, &cursor, kind, now); err != nil {
					return TrayEvents{}, err
				}
			}
			continue
		}
		if !until.After(after) {
			continue
		}
		// The second of a time set to the present is read again, without
		// the records that were there then.
		seen, started := cursor.Seen[kind]
		from := after
		if started {
			from = after.Add(-time.Second)
		}
		include := func(record state.FeedRecord) bool {
			if started && (record.At.Before(after) || record.At.Equal(after) && slices.Contains(seen, recordMark(record.ID))) {
				return false
			}
			return !app.dropped(feed, originKey(kind, record.ID))
		}
		records, total, err := app.Store.FeedRecords(ctx, kind, from, until, include, trayFeedSeparate+1)
		if err != nil {
			return TrayEvents{}, err
		}
		events.Notifications = append(events.Notifications, app.recordNotifications(feed, kind, records, total)...)
		cursor.Times[kind] = until.Unix()
		delete(cursor.Seen, kind)
	}
	if update != "" && update != cursor.Update {
		if feed.shows(state.NotifyUpdate) {
			events.Notifications = append(events.Notifications, app.updateNotification(feed.lang, update))
		}
		cursor.Update = update
	}
	events.Cursor = cursor.encode()
	return events, nil
}

// pushNotifications turns the pushes after the cursor's sequence into
// notifications and returns the sequence the next read starts after.
// Pushes group from the first one the icon shows: every push within
// trayFeedDelay of it is in its notification, which waits until that time
// has passed. Pushes the icon does not show are passed over.
func (app *App) pushNotifications(request *http.Request, feed trayFeedRequest, pushes []state.PushEvent, after, last int64) ([]TrayNotification, int64) {
	notifications := []TrayNotification{}
	if !feed.shows(state.NotifyPush) {
		return notifications, last
	}
	now := app.now()
	for index := 0; index < len(pushes); {
		if app.dropped(feed, pushOrigin(pushes[index].Sequence)) {
			after = pushes[index].Sequence
			index++
			continue
		}
		first := pushes[index].PushedAt
		if now.Before(first.Add(trayFeedDelay)) {
			break
		}
		group := []state.PushEvent{}
		for ; index < len(pushes) && pushes[index].PushedAt.Before(first.Add(trayFeedDelay)); index++ {
			if !app.dropped(feed, pushOrigin(pushes[index].Sequence)) {
				group = append(group, pushes[index])
			}
			after = pushes[index].Sequence
		}
		notifications = append(notifications, app.pushNotification(request, feed, group))
	}
	return notifications, after
}

// pushNotification words one group of pushes.
func (app *App) pushNotification(request *http.Request, feed trayFeedRequest, group []state.PushEvent) TrayNotification {
	text := func(code webui.MessageCode, args ...any) string {
		return fmt.Sprintf(webui.Text(feed.lang, code), args...)
	}
	latest := group[len(group)-1]
	notification := TrayNotification{
		ID:   fmt.Sprintf("push:%d-%d", group[0].Sequence, latest.Sequence),
		Kind: state.NotifyPush,
	}
	if feed.onlyOthers {
		notification.Subtitle = text(webui.MsgNotifyPushedElsewhere)
	}
	name := shortRefName(latest.Ref)
	subject := app.commitSubject(request, latest)
	if len(group) > 1 {
		counts, repositories := map[string]int{}, []string{}
		for _, push := range group {
			if counts[push.RepositoryName] == 0 {
				repositories = append(repositories, push.RepositoryName)
			}
			counts[push.RepositoryName]++
		}
		parts := []string{}
		for _, repository := range repositories {
			parts = append(parts, text(webui.MsgNotifyRepositoryPushes, repository, counts[repository]))
		}
		notification.Title = text(webui.MsgNotifyPushes, len(group))
		latestLine := latest.RepositoryName + " " + name
		if subject != "" {
			latestLine += ", " + subject
		}
		notification.Body = strings.Join(parts, ", ") + ". " + text(webui.MsgNotifyLatest, latestLine)
		notification.Path = "/activity"
		if len(repositories) == 1 {
			notification.Path = repositoryPath(latest.RepositoryID) + "/commits?ref=" + url.QueryEscape(name)
		}
		return notification
	}
	push := latest
	notification.Path = repositoryPath(push.RepositoryID) + "/commits?ref=" + url.QueryEscape(name)
	branch := strings.HasPrefix(push.Ref, "refs/heads/")
	switch {
	case push.NewOID == "":
		notification.Title = text(webui.MsgNotifyRefDeleted, name, push.RepositoryName)
		notification.Path = repositoryPath(push.RepositoryID)
	case push.OldOID == "" && branch:
		notification.Title = text(webui.MsgNotifyNewBranch, name, push.RepositoryName)
		notification.Body = subject
	case push.OldOID == "" && strings.HasPrefix(push.Ref, "refs/tags/"):
		notification.Title = text(webui.MsgNotifyNewTag, name, push.RepositoryName)
		notification.Body = subject
	case push.OldOID == "":
		notification.Title = text(webui.MsgNotifyNewRef, name, push.RepositoryName)
		notification.Body = subject
	default:
		count, err := app.Repositories.CommitsBetween(request.Context(), push.RepositoryID, push.OldOID, push.NewOID)
		switch {
		case err != nil:
			log.Printf("the notification of a push to repository %q does not count its commits: %s", push.RepositoryID, logtext.Cause(err))
			notification.Title = text(webui.MsgNotifyRefUpdated, name, push.RepositoryName)
		case count == 1:
			notification.Title = text(webui.MsgNotifyCommit, push.RepositoryName)
		case count > 1:
			notification.Title = text(webui.MsgNotifyCommits, count, push.RepositoryName)
		default:
			notification.Title = text(webui.MsgNotifyRefUpdated, name, push.RepositoryName)
		}
		notification.Body = name
		if subject != "" {
			notification.Body = name + ": " + subject
			if count > 1 {
				notification.Body = text(webui.MsgNotifyAndMore, notification.Body, count-1)
			}
		}
	}
	return notification
}

// commitSubject returns the subject of the commit a push left its ref at,
// or "" when it deleted the ref or the commit cannot be read, which is
// logged.
func (app *App) commitSubject(request *http.Request, push state.PushEvent) string {
	if push.NewOID == "" {
		return ""
	}
	commits, err := app.Repositories.CommitsAt(request.Context(), push.RepositoryID, push.NewOID, 1)
	if err != nil || len(commits) == 0 {
		log.Printf("the notification of a push to repository %q leaves out its commit message: %s", push.RepositoryID, logtext.Cause(err))
		return ""
	}
	return commits[0].Subject
}

// shortRefName is a ref's name without refs/heads/ or refs/tags/.
func shortRefName(ref string) string {
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		if name, found := strings.CutPrefix(ref, prefix); found {
			return name
		}
	}
	return ref
}

func repositoryPath(repositoryID string) string {
	return "/repositories/" + url.PathEscape(repositoryID)
}

// recordNotifications words the records of one kind that the icon shows,
// total of them of which records are the first: one notification each, or
// one for all when there are more than trayFeedSeparate.
func (app *App) recordNotifications(feed trayFeedRequest, kind string, records []state.FeedRecord, total int) []TrayNotification {
	text := func(code webui.MessageCode, args ...any) string {
		return fmt.Sprintf(webui.Text(feed.lang, code), args...)
	}
	notifications := []TrayNotification{}
	if count := total; count > trayFeedSeparate {
		last := records[len(records)-1]
		summary := TrayNotification{ID: originKey(kind, last.ID) + "+" + strconv.Itoa(count), Kind: kind, Path: "/activity"}
		summary.Title = text(map[string]webui.MessageCode{
			state.NotifyPullRequest: webui.MsgNotifyPullRequests, state.NotifyCheckFailed: webui.MsgNotifyChecksFailed,
			state.NotifyImportFailed: webui.MsgNotifyImportsFailed, state.NotifyBackupFailed: webui.MsgNotifyBackupsFailed,
		}[kind], count)
		return append(notifications, summary)
	}
	for _, record := range records {
		notification := TrayNotification{ID: originKey(kind, record.ID), Kind: kind, Body: record.Message}
		switch kind {
		case state.NotifyPullRequest:
			notification.Title = text(webui.MsgNotifyPullRequest, record.Number, record.RepositoryName)
			notification.Body = record.Title
			notification.Path = repositoryPath(record.RepositoryID) + "/pull-requests/" + strconv.Itoa(record.Number)
			if feed.onlyOthers {
				notification.Subtitle = text(webui.MsgNotifyOpenedElsewhere)
			}
		case state.NotifyCheckFailed:
			notification.Title = text(webui.MsgNotifyCheckFailed, record.RepositoryName)
			notification.Subtitle = shortRefName(record.Branch)
			notification.Path = repositoryPath(record.RepositoryID) + "/tasks"
			if record.Number > 0 {
				notification.Subtitle = text(webui.MsgNotifyForPullRequest, record.Number)
				notification.Path = repositoryPath(record.RepositoryID) + "/pull-requests/" + strconv.Itoa(record.Number)
			}
		case state.NotifyImportFailed:
			notification.Title = text(webui.MsgNotifyImportFailed, record.RepositoryName)
			notification.Path = repositoryPath(record.RepositoryID) + "/import"
		case state.NotifyBackupFailed:
			notification.Title = text(webui.MsgNotifyBackupFailed)
			notification.Path = "/settings"
		}
		notifications = append(notifications, notification)
	}
	return notifications
}

// updateNotification tells of a newer release and the command that
// installs it.
func (app *App) updateNotification(lang webui.Lang, version string) TrayNotification {
	notification := TrayNotification{
		ID: state.NotifyUpdate + ":" + version, Kind: state.NotifyUpdate, Path: "/",
		Title: fmt.Sprintf(webui.Text(lang, webui.MsgNotifyUpdate), version),
		Body:  webui.Text(lang, webui.MsgTrayUpdateGuide),
	}
	if app.UpdateCommand != nil {
		if command, _, _ := app.UpdateCommand(version); command != "" {
			notification.Body = webui.Text(lang, webui.MsgTrayUpdateRun) + " " + command
		}
	}
	return notification
}
