package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"owngit/internal/auth"
	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// Browser approval for first-run setup in an interactive terminal.
//
// When `owngit serve` asks the setup questions in its terminal, the owner can
// continue in a browser instead. The browser asks for approval, which binds
// a request to an HttpOnly, SameSite=Strict cookie of that browser, and
// shows a short comparison code. Only the terminal of the serving process
// can approve, through SetupApprovals; no HTTP request can. An approved
// request is redeemed once by the same browser for a setup session, exactly
// what redeeming the setup file grants. The code only helps the owner
// recognize the browser; it is not a credential.

const (
	approvalCookie = "owngit_setup_request"
	// approvalLifetime bounds a pending request and the time an approved
	// request may wait to be redeemed.
	approvalLifetime = 10 * time.Minute
	// approvalWindow and approvalLimit bound new requests from one address.
	approvalWindow = 10 * time.Minute
	approvalLimit  = 5
	// approvalCooldown is how long a rejected browser waits before it may
	// ask again.
	approvalCooldown = time.Minute
	// approvalAlphabet has no characters that are easy to confuse, such as
	// 0 and O, 1 and I, 2 and Z, 5 and S, or 8 and B.
	approvalAlphabet = "3469ACDEFGHJKMNPQRTUVWXY"
)

// ApprovalRequest is a pending browser request as the terminal shows it.
type ApprovalRequest struct {
	ID string
	// Code is the comparison code, for example "K7Q-4MP".
	Code string
	// Address is the established client address, or the nearest proxy address
	// when the original address is unknown.
	Address string
	// Loopback is true only when the direct connection proves the request came
	// from this computer.
	Loopback bool
	// ForwardedUnknown is true when a trusted proxy forwarded the request but
	// did not establish the original address.
	ForwardedUnknown bool
}

// ErrApprovalGone means the request was no longer waiting: it expired, was
// replaced, or setup ended.
var ErrApprovalGone = errors.New("the browser request is no longer waiting")

type approvalStatus string

const (
	approvalNone     approvalStatus = "none"
	approvalPending  approvalStatus = "pending"
	approvalApproved approvalStatus = "approved"
	approvalRejected approvalStatus = "rejected"
	approvalExpired  approvalStatus = "expired"
)

type approvalRequest struct {
	ApprovalRequest
	cookie   [32]byte // SHA-256 of the browser's request cookie
	status   approvalStatus
	deadline time.Time
}

// SetupApprovals holds the browser approval state of one serving process.
// It exists only when first-run setup runs in the terminal. When the
// terminal turns out to be unusable, Abandon hands setup to the setup file,
// and browsers are served exactly as if approval never existed.
type SetupApprovals struct {
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu        sync.Mutex
	open      bool
	void      bool
	abandoned bool
	current   *approvalRequest
	rejected  map[[32]byte]time.Time
	recent    map[string][]time.Time
	changed   chan struct{}
	done      chan struct{}
}

// NewSetupApprovals returns approval state that accepts no request until the
// terminal opens it.
func NewSetupApprovals() *SetupApprovals {
	return &SetupApprovals{
		rejected: make(map[[32]byte]time.Time), recent: make(map[string][]time.Time),
		changed: make(chan struct{}, 1), done: make(chan struct{}),
	}
}

func (approvals *SetupApprovals) now() time.Time {
	if approvals.Now != nil {
		return approvals.Now()
	}
	return time.Now()
}

func (approvals *SetupApprovals) signal() {
	select {
	case approvals.changed <- struct{}{}:
	default:
	}
}

// Changed is signalled when a request arrives or is withdrawn.
func (approvals *SetupApprovals) Changed() <-chan struct{} { return approvals.changed }

// Done is closed when setup is complete, by any path.
func (approvals *SetupApprovals) Done() <-chan struct{} { return approvals.done }

// Open starts accepting browser requests: the terminal is waiting for one.
func (approvals *SetupApprovals) Open() {
	approvals.mu.Lock()
	defer approvals.mu.Unlock()
	approvals.open = !approvals.void
}

// Shut stops accepting requests and voids the current one, pending or
// approved.
func (approvals *SetupApprovals) Shut() {
	approvals.mu.Lock()
	defer approvals.mu.Unlock()
	approvals.open = false
	approvals.current = nil
	approvals.signal()
}

// Abandon ends browser approval because setup continues with the setup
// file instead of the terminal.
func (approvals *SetupApprovals) Abandon() {
	approvals.mu.Lock()
	defer approvals.mu.Unlock()
	approvals.abandoned, approvals.open, approvals.current = true, false, nil
	approvals.signal()
}

// Active reports whether browsers must ask the terminal for approval: the
// approval state exists and was not abandoned. It is false for nil.
func (approvals *SetupApprovals) Active() bool {
	if approvals == nil {
		return false
	}
	approvals.mu.Lock()
	defer approvals.mu.Unlock()
	return !approvals.abandoned
}

// Void ends browser approval for good. Setup completion calls it.
func (approvals *SetupApprovals) Void() {
	approvals.mu.Lock()
	defer approvals.mu.Unlock()
	if approvals.void {
		return
	}
	approvals.void, approvals.open, approvals.current = true, false, nil
	close(approvals.done)
	approvals.signal()
}

// live returns the current request if it has not expired, clearing it
// otherwise. The caller holds the lock.
func (approvals *SetupApprovals) live() *approvalRequest {
	if approvals.current != nil && !approvals.now().Before(approvals.current.deadline) {
		approvals.current = nil
	}
	return approvals.current
}

// Pending returns the request waiting for the terminal's answer.
func (approvals *SetupApprovals) Pending() (ApprovalRequest, bool) {
	approvals.mu.Lock()
	defer approvals.mu.Unlock()
	if current := approvals.live(); current != nil && current.status == approvalPending {
		return current.ApprovalRequest, true
	}
	return ApprovalRequest{}, false
}

// Decide approves or rejects the pending request id. Only the terminal of
// the serving process calls it. A rejected browser cannot continue, and it
// must wait before asking again.
func (approvals *SetupApprovals) Decide(id string, approve bool) error {
	approvals.mu.Lock()
	defer approvals.mu.Unlock()
	current := approvals.live()
	if current == nil || current.ID != id || current.status != approvalPending {
		return ErrApprovalGone
	}
	now := approvals.now()
	if approve {
		current.status = approvalApproved
		current.deadline = now.Add(approvalLifetime)
		return nil
	}
	approvals.rejected[current.cookie] = now
	approvals.current = nil
	return nil
}

// approvalRefusal says why a browser request was not accepted.
type approvalRefusal struct {
	status int
	reason webui.MessageCode
}

// request records a new request from address for the browser whose cookie
// hashes to cookie. The same browser asking again gets its current request.
func (approvals *SetupApprovals) request(address string, provenance requestctx.ClientProvenance, cookie [32]byte) (ApprovalRequest, *approvalRefusal) {
	approvals.mu.Lock()
	defer approvals.mu.Unlock()
	now := approvals.now()
	approvals.prune(now)
	current := approvals.live()
	switch {
	case approvals.void:
		return ApprovalRequest{}, &approvalRefusal{http.StatusConflict, webui.MsgSetupAlreadyDone}
	case !approvals.open:
		return ApprovalRequest{}, &approvalRefusal{http.StatusConflict, webui.MsgSetupApprovalNotWaiting}
	case current != nil && current.cookie == cookie:
		return current.ApprovalRequest, nil
	case current != nil:
		return ApprovalRequest{}, &approvalRefusal{http.StatusConflict, webui.MsgSetupApprovalBusy}
	case len(approvals.recent[address]) >= approvalLimit:
		return ApprovalRequest{}, &approvalRefusal{http.StatusTooManyRequests, webui.MsgSetupApprovalLimited}
	}
	id, code := auth.RandomToken(16), approvalCode()
	ip := net.ParseIP(address)
	approvals.current = &approvalRequest{
		ApprovalRequest: ApprovalRequest{
			ID: id, Code: code, Address: address,
			Loopback:         provenance == requestctx.ClientDirect && ip != nil && ip.IsLoopback(),
			ForwardedUnknown: provenance == requestctx.ClientForwardedUnknown,
		},
		cookie: cookie, status: approvalPending, deadline: now.Add(approvalLifetime),
	}
	approvals.recent[address] = append(approvals.recent[address], now)
	approvals.signal()
	return approvals.current.ApprovalRequest, nil
}

// prune forgets rate-limit and rejection records that no longer matter, so
// the maps stay small. The caller holds the lock.
func (approvals *SetupApprovals) prune(now time.Time) {
	for address, times := range approvals.recent {
		kept := times[:0]
		for _, at := range times {
			if now.Sub(at) < approvalWindow {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(approvals.recent, address)
		} else {
			approvals.recent[address] = kept
		}
	}
	for cookie, at := range approvals.rejected {
		if now.Sub(at) >= approvalCooldown {
			delete(approvals.rejected, cookie)
		}
	}
}

// status reports the state of the request of the browser whose cookie
// hashes to cookie, and its code while it is pending.
func (approvals *SetupApprovals) status(cookie [32]byte) (approvalStatus, string) {
	approvals.mu.Lock()
	defer approvals.mu.Unlock()
	if at, rejected := approvals.rejected[cookie]; rejected && approvals.now().Sub(at) < approvalCooldown {
		return approvalRejected, ""
	}
	if approvals.current != nil && approvals.current.cookie == cookie {
		if approvals.live() == nil {
			return approvalExpired, ""
		}
		return approvals.current.status, approvals.current.Code
	}
	return approvalNone, ""
}

// redeem consumes the approved request of the browser whose cookie hashes to
// cookie. It succeeds once.
func (approvals *SetupApprovals) redeem(cookie [32]byte) bool {
	approvals.mu.Lock()
	defer approvals.mu.Unlock()
	current := approvals.live()
	if current == nil || current.cookie != cookie || current.status != approvalApproved {
		return false
	}
	approvals.current = nil
	approvals.signal()
	return true
}

// approvalCode returns a random code such as "K7Q-4MP", each character
// drawn uniformly from approvalAlphabet. A random byte picks a character by
// its remainder; a byte at or above the largest multiple of the alphabet's
// length is drawn again, so every character has the same number of bytes.
// crypto/rand.Read cannot fail: it crashes the program instead (Go 1.24).
func approvalCode() string {
	code := make([]byte, 0, 7)
	accepted := 256 - 256%len(approvalAlphabet)
	var random [1]byte
	for len(code) < cap(code) {
		if len(code) == 3 {
			code = append(code, '-')
			continue
		}
		rand.Read(random[:])
		if int(random[0]) < accepted {
			code = append(code, approvalAlphabet[int(random[0])%len(approvalAlphabet)])
		}
	}
	return string(code)
}

func requestClient(request *http.Request) (string, requestctx.ClientProvenance) {
	info := requestctx.Of(request)
	address := info.ClientAddress
	if ip := net.ParseIP(address); ip != nil {
		address = ip.String()
	}
	return address, info.ClientProvenance
}

func (app *App) approvalCookieHash(request *http.Request) ([32]byte, bool) {
	cookie, err := request.Cookie(cookieNameForScheme(request, approvalCookie))
	if err != nil || cookie.Value == "" || len(cookie.Value) > 256 {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(cookie.Value)), true
}

// handleSetupApprovalPage is /setup for a browser without a setup session
// while the terminal runs setup. An approved browser is given its setup
// session here, once.
func (app *App) handleSetupApprovalPage(writer http.ResponseWriter, request *http.Request) {
	page := webui.SetupPage{
		Stage: webui.SetupApproval, ApprovalURL: "/setup/approval", RedeemURL: "/setup/redeem", SetupURL: "/setup",
		Prerequisites: app.setupPrerequisites(),
	}
	status := http.StatusOK
	hash, hasCookie := app.approvalCookieHash(request)
	approval, code := approvalNone, ""
	if hasCookie {
		approval, code = app.Approvals.status(hash)
	}
	switch approval {
	case approvalApproved:
		started, err := app.startApprovedSession(writer, request, hash)
		switch {
		case errors.Is(err, state.ErrSetupComplete):
			// Another browser or the terminal finished setup meanwhile.
			app.clearCookie(writer, request, cookieNameForScheme(request, approvalCookie))
			page.Stage, page.Reason, status = webui.SetupUnavailable, webui.MsgSetupAlreadyDone, http.StatusConflict
		case err != nil:
			// The cookie stays: an approval that was not used up can still be
			// redeemed from the retry link, and a used one is refused there.
			page.Stage, page.Reason, status = webui.SetupUnavailable, webui.MsgErrUnavailable, unavailable(request, "approved setup session", err)
		case started:
			http.Redirect(writer, request, "/setup", http.StatusSeeOther)
			return
		default:
			app.clearCookie(writer, request, cookieNameForScheme(request, approvalCookie))
			page.Stage, page.Reason, status = webui.SetupUnavailable, webui.MsgSetupApprovalExpired, http.StatusGone
		}
	case approvalPending:
		page.Stage, page.ApprovalCode = webui.SetupApprovalWait, code
	case approvalRejected:
		// The cookie stays until the wait is over, so this browser cannot ask
		// again at once.
		page.Stage, page.Reason, status = webui.SetupUnavailable, webui.MsgSetupApprovalRejected, http.StatusForbidden
	case approvalExpired:
		app.clearCookie(writer, request, cookieNameForScheme(request, approvalCookie))
		page.Stage, page.Reason, status = webui.SetupUnavailable, webui.MsgSetupApprovalExpired, http.StatusGone
	default:
		if hasCookie {
			app.clearCookie(writer, request, cookieNameForScheme(request, approvalCookie))
		}
	}
	if page.Stage == webui.SetupUnavailable {
		page.RetryURL = "/setup"
	}
	csrf := app.preauthCSRF(writer, request)
	chrome, err := app.chrome(writer, request, webui.SectionSetup, "", csrf)
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	page.Chrome = chrome
	writer.Header().Set("Cache-Control", "no-store")
	app.render(writer, request, status, page)
}

// startApprovedSession redeems the approval for a setup session, like
// redeeming the setup file. It reports false when the approval can no
// longer be redeemed, and an error when the session could not be started.
// The approval is used up once redeemed, even if saving the session fails.
func (app *App) startApprovedSession(writer http.ResponseWriter, request *http.Request, hash [32]byte) (bool, error) {
	sessionToken, csrf := auth.RandomToken(32), auth.RandomToken(32)
	if !app.Approvals.redeem(hash) {
		return false, nil
	}
	expires := app.now().Add(20 * time.Minute)
	if err := app.Store.StartApprovedSetupSession(request.Context(), sessionToken, csrf, expires); err != nil {
		return false, err
	}
	app.setCookie(writer, request, cookieNameForScheme(request, setupCookie), sessionToken, expires, true)
	app.clearCookie(writer, request, cookieNameForScheme(request, approvalCookie))
	app.clearCookie(writer, request, cookieNameForScheme(request, preauthCookie))
	return true, nil
}

// handleSetupApprovalRequest asks the terminal to approve this browser.
func (app *App) handleSetupApprovalRequest(writer http.ResponseWriter, request *http.Request) {
	if !app.parseForm(writer, request) {
		return
	}
	if !app.validPreauthCSRF(request, postValue(request, "csrf")) {
		app.renderError(writer, request, http.StatusForbidden, webui.MsgErrCSRF, "")
		return
	}
	if !app.Approvals.Active() {
		app.renderError(writer, request, http.StatusNotFound, webui.MsgErrNotFound, request.URL.Path)
		return
	}
	// The browser's current request is kept if it asks again; otherwise the
	// new request gets a new cookie value, never one the browser supplied.
	if hash, ok := app.approvalCookieHash(request); ok {
		switch current, _ := app.Approvals.status(hash); current {
		case approvalPending:
			http.Redirect(writer, request, "/setup", http.StatusSeeOther)
			return
		case approvalRejected:
			app.renderApprovalRefusal(writer, request, approvalRefusal{http.StatusTooManyRequests, webui.MsgSetupApprovalRejected})
			return
		}
	}
	token := auth.RandomToken(32)
	address, provenance := requestClient(request)
	_, refusal := app.Approvals.request(address, provenance, sha256.Sum256([]byte(token)))
	if refusal != nil {
		app.renderApprovalRefusal(writer, request, *refusal)
		return
	}
	app.setCookie(writer, request, cookieNameForScheme(request, approvalCookie), token, app.now().Add(approvalLifetime), true)
	http.Redirect(writer, request, "/setup", http.StatusSeeOther)
}

func (app *App) renderApprovalRefusal(writer http.ResponseWriter, request *http.Request, refusal approvalRefusal) {
	chrome, err := app.chrome(writer, request, webui.SectionSetup, "", "")
	if err != nil {
		app.answerUnavailable(writer, request, "page frame read", err)
		return
	}
	app.render(writer, request, refusal.status, webui.SetupPage{
		Chrome: chrome, Stage: webui.SetupUnavailable, Reason: refusal.reason, RetryURL: "/setup",
	})
}

// handleSetupApprovalStatus tells the waiting page whether the terminal has
// answered. It reports only the state of this browser's own request and
// redeems nothing; the page then opens /setup, which does.
func (app *App) handleSetupApprovalStatus(writer http.ResponseWriter, request *http.Request, settings state.Settings) {
	status := approvalNone
	switch {
	case settings.Initialized:
		status = "done"
	case app.Approvals.Active():
		if hash, ok := app.approvalCookieHash(request); ok {
			status, _ = app.Approvals.status(hash)
		}
	}
	writeAPIJSON(writer, http.StatusOK, map[string]string{"state": string(status)})
}

// EndBrowserSetup stops browser approval and ends browser setup sessions,
// when the owner continues setup in the terminal instead.
func (app *App) EndBrowserSetup(ctx context.Context) error {
	if app.Approvals != nil {
		app.Approvals.Shut()
	}
	return app.Store.EndSetupSessions(ctx)
}
