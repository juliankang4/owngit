package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"owngit/internal/githttp"
	"owngit/internal/logtext"
	"owngit/internal/releasecheck"
	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// TrayStatusPath is the status the tray icon of this computer reads. It
// answers only a request that comes straight from this computer, not through
// a proxy, and that carries the token of state.TrayAccessFile, which only
// the account that runs OwnGit can read. So neither another device nor
// another account on this computer learns the recent pushes it lists.
// Every answer carries the proof of the request's nonce (state.TrayProof),
// so the icon knows it came from this server. HealthPath stays without
// data.
const TrayStatusPath = "/tray/status"

// trayPushes is how many recent pushes the tray status lists.
const trayPushes = 3

// trayCheckupReuse is how long the tray status reuses a checkup, which runs
// system tools, so an icon that polls does not run them on every read.
const trayCheckupReuse = time.Minute

// trayCheckupTimeout bounds a checkup the tray status runs. Each tool of the
// checkup has its own shorter bound.
const trayCheckupTimeout = time.Minute

// TrayStatus is the answer of TrayStatusPath.
type TrayStatus struct {
	OK bool `json:"ok"`
	// State is "running", or "attention" when the owner has something to
	// do: finish setup, install a newer release, or repair a problem the
	// checkup found.
	State   string `json:"state"`
	Version string `json:"version"`
	// Shown is false when the owner hid the icon, from the dashboard or
	// with "owngit tray off".
	Shown bool `json:"shown"`
	// DashboardURL and CloneAddress are the addresses the dashboard shows:
	// the configured base URL, or else the address of this request.
	DashboardURL  string `json:"dashboard_url"`
	CloneAddress  string `json:"clone_address"`
	SetupRequired bool   `json:"setup_required"`
	// Update is the newer release, when the update check found one.
	Update   *TrayUpdate   `json:"update"`
	Findings []TrayFinding `json:"findings"`
	// Pushes are the latest successful pushes, the newest first.
	Pushes []TrayPush `json:"pushes"`
}

// TrayUpdate is a newer release and the one command that installs it on
// this computer, as the dashboard shows it to an administrator.
type TrayUpdate struct {
	Version  string `json:"version"`
	NotesURL string `json:"notes_url"`
	// Command is "" when this install route has no command.
	Command string `json:"command"`
	// Start is the program to start afterwards when no service starts
	// OwnGit, and Restart asks to restart OwnGit where it runs.
	Start   string `json:"start"`
	Restart bool   `json:"restart"`
	// GuideURL explains how each install route updates, for a route
	// without a command.
	GuideURL string `json:"guide_url"`
}

// TrayFinding is one result of the checkup, in the language the request
// asked for.
type TrayFinding struct {
	Code    webui.MessageCode `json:"code"`
	Message string            `json:"message"`
	Repair  string            `json:"repair"`
	// Unchecked is a check that could not run; it alone does not ask for
	// attention.
	Unchecked bool `json:"unchecked"`
}

// TrayPush is one successful push.
type TrayPush struct {
	RepositoryID string `json:"repository_id"`
	Repository   string `json:"repository"`
	// Ref is one ref the push updated, preferably a branch; Branch is its
	// branch name, or "" when it is not a branch.
	Ref         string `json:"ref"`
	Branch      string `json:"branch"`
	RefsUpdated int    `json:"refs_updated"`
	// PushedAt is when OwnGit received the push.
	PushedAt time.Time   `json:"pushed_at"`
	Actor    state.Actor `json:"actor"`
	// ActorLabel names how the push was authorized, in the language the
	// request asked for, or the helper credential's label.
	ActorLabel string `json:"actor_label"`
}

// trayCheckup keeps the last checkup the tray status ran.
type trayCheckup struct {
	mu       sync.Mutex
	at       time.Time
	findings []webui.Finding
}

func (app *App) handleTrayStatus(writer http.ResponseWriter, request *http.Request) {
	if app.TrayToken == "" || app.TrayProof == "" {
		writeAPIError(writer, http.StatusNotFound, "not_found", "Not found.", nil)
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", "GET")
		writeAPIError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET.", nil)
		return
	}
	if !fromThisComputer(request) {
		writeAPIError(writer, http.StatusForbidden, "not_local", "The tray status answers only programs on the computer that runs OwnGit.", nil)
		return
	}
	token, found := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
	sent, want := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(app.TrayToken))
	if !found || subtle.ConstantTimeCompare(sent[:], want[:]) != 1 {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="OwnGit tray"`)
		writeAPIError(writer, http.StatusUnauthorized, "unauthorized", "Send the token of the tray access file.", nil)
		return
	}
	nonce := request.Header.Get(state.TrayNonceHeader)
	if !state.ValidTrayNonce(nonce) {
		writeAPIError(writer, http.StatusBadRequest, "invalid_nonce", "Send a new nonce in "+state.TrayNonceHeader+".", nil)
		return
	}
	status, err := app.trayStatus(request)
	if err != nil {
		writeAPIError(writer, unavailable(request, "tray status read", err), "status_unavailable", "OwnGit runs but could not read its status. The OwnGit log says why.", nil)
		return
	}
	code, body := encodeAPIJSON(http.StatusOK, status)
	writer.Header().Set(state.TrayProofHeader, state.TrayProof(app.TrayProof, nonce, body))
	writeEncodedAPIJSON(writer, code, body)
}

// fromThisComputer reports whether request came over a direct connection
// from this computer: from a loopback address or from the address it
// reached, and not through a proxy, which would forward others' requests.
func fromThisComputer(request *http.Request) bool {
	info := requestctx.Of(request)
	if info.FromProxy || request.Header.Get("X-Forwarded-For") != "" || request.Header.Get("Forwarded") != "" {
		return false
	}
	if loopbackPeer(info.Peer) {
		return true
	}
	peer, err := netip.ParseAddrPort(info.Peer)
	local, ok := request.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if err != nil || !ok {
		return false
	}
	own, err := netip.ParseAddrPort(local.String())
	return err == nil && own.Addr().Unmap() == peer.Addr().Unmap()
}

func (app *App) trayStatus(request *http.Request) (TrayStatus, error) {
	ctx := request.Context()
	lang, _ := webui.ParseLang(request.URL.Query().Get("lang"))
	settings, err := app.Store.Settings(ctx)
	if err != nil {
		return TrayStatus{}, err
	}
	events, err := app.Store.RecentPushes(ctx, trayPushes)
	if err != nil {
		return TrayStatus{}, err
	}
	origin := app.serverOrigin(request)
	hidden, err := app.trayHidden()
	if err != nil {
		return TrayStatus{}, err
	}
	status := TrayStatus{
		OK: true, State: "running", Version: app.Version, Shown: !hidden, DashboardURL: origin, CloneAddress: origin + "/git/",
		SetupRequired: !settings.Initialized, Findings: []TrayFinding{}, Pushes: []TrayPush{},
	}
	if app.Releases != nil && settings.UpdateCheck {
		if release, newer := app.Releases.Newer(); newer {
			status.Update = &TrayUpdate{Version: release.Version, NotesURL: release.NotesURL, GuideURL: releasecheck.UpdateGuideURL}
			if app.UpdateCommand != nil {
				status.Update.Command, status.Update.Start, status.Update.Restart = app.UpdateCommand(release.Version)
			}
		}
	}
	attention := status.SetupRequired || status.Update != nil
	if settings.Initialized {
		for _, finding := range app.trayFindings(ctx) {
			status.Findings = append(status.Findings, TrayFinding{Code: finding.Code, Message: finding.Sentence(lang), Repair: finding.Repair, Unchecked: finding.Unchecked})
			attention = attention || !finding.Unchecked
		}
	}
	if attention {
		status.State = "attention"
	}
	for _, event := range events {
		push := TrayPush{
			RepositoryID: event.RepositoryID, Repository: event.RepositoryName, Ref: event.Ref,
			RefsUpdated: event.RefsUpdated, PushedAt: event.PushedAt.UTC(), Actor: event.Actor, ActorLabel: actorLabel(event.Actor, lang),
		}
		push.Branch, _ = strings.CutPrefix(event.Ref, "refs/heads/")
		if push.Branch == event.Ref {
			push.Branch = ""
		}
		status.Pushes = append(status.Pushes, push)
	}
	return status, nil
}

// trayFindings returns the checkup of this computer, run at most once per
// trayCheckupReuse. The checkup runs apart from the request that asked for
// it, so a request that ends early cannot leave checks that could not run
// for the next minute.
func (app *App) trayFindings(ctx context.Context) []webui.Finding {
	if app.Diagnose == nil {
		return nil
	}
	app.trayCheckup.mu.Lock()
	defer app.trayCheckup.mu.Unlock()
	if app.trayCheckup.at.IsZero() || app.now().Sub(app.trayCheckup.at) >= trayCheckupReuse {
		checkupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), trayCheckupTimeout)
		defer cancel()
		app.trayCheckup.findings, app.trayCheckup.at = app.Diagnose(checkupContext), app.now()
	}
	return app.trayCheckup.findings
}

// actorLabel names who made a change as the tray shows it.
func actorLabel(actor state.Actor, lang webui.Lang) string {
	switch actor.Kind {
	case state.ActorAccess:
		return webui.Text(lang, webui.MsgActorAccess)
	case state.ActorAdministrator:
		return webui.Text(lang, webui.MsgActorAdministrator)
	}
	return actor.Label
}

// RecordPush records a push that updated refs, for the tray. It is the Git
// handler's OnPush. Every push is authorized by general access. The push has
// already succeeded, so a failure to record it is logged and changes
// nothing else.
func (app *App) RecordPush(ctx context.Context, repositoryID string, updates []githttp.RefUpdate) {
	shown := updates[0]
	for _, update := range updates {
		if strings.HasPrefix(update.Ref, "refs/heads/") {
			shown = update
			break
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := app.Store.RecordPush(ctx, state.PushEvent{
		RepositoryID: repositoryID, Ref: shown.Ref, OldOID: shown.Old, NewOID: shown.New,
		RefsUpdated: len(updates), Actor: generalAccessActor, PushedAt: app.now(),
	})
	if err != nil {
		log.Printf("push to repository %q was not recorded for the tray: %s", repositoryID, logtext.Cause(err))
	}
}

// trayInfo is the icon block of the Settings page. A choice that cannot be
// read is shown as such and logged, not as shown or hidden.
func (app *App) trayInfo(request *http.Request) webui.TrayInfo {
	if !app.TrayAvailable {
		return webui.TrayInfo{Unavailable: true}
	}
	info := webui.TrayInfo{Desktop: app.TrayDesktop != nil && app.TrayDesktop()}
	hidden, err := app.trayHidden()
	if err != nil {
		logFailure(request, "tray icon choice read", err)
		info.Unreadable = true
		return info
	}
	info.Shown = !hidden
	return info
}

// trayHidden reports whether the owner hid the icon of this computer.
func (app *App) trayHidden() (bool, error) {
	held, err := state.OpenStateDirectory(app.Store.Dir())
	if err != nil {
		return false, err
	}
	defer held.Close()
	return state.TrayHidden(held)
}

// setTrayHidden hides the icon of this computer or shows it again.
func (app *App) setTrayHidden(hidden bool) error {
	held, err := state.OpenStateDirectory(app.Store.Dir())
	if err != nil {
		return err
	}
	defer held.Close()
	return state.SetTrayHidden(held, hidden)
}
