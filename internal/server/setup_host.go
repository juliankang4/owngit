package server

import (
	"crypto/sha256"
	"net/http"
	"sync"

	"owngit/internal/requestctx"
	"owngit/internal/state"
)

// Setup from an unknown Host.
//
// The Host check refuses a name the server was not told about, which also
// stops DNS rebinding: a page on another name that resolves to this server
// reaches it with its own name as Host. Before setup is complete, one
// exception lets the owner open the setup link by an address the server was
// not started with, such as its LAN address typed on another device:
//
//   - GET /setup answers a redemption page that shows no server state (no
//     prerequisites, version, addresses, suggested folder or approval), and
//     GET or HEAD of the static files that page loads is served;
//   - POST /setup/redeem accepts the setup capability, which is only in the
//     owner-only setup file. A successful redemption binds the new setup
//     session to that Host;
//   - GET and POST /setup continue only with the session bound to the same
//     Host. Finish setup keeps the Host only when the owner ticks "keep".
//
// Everything else from an unknown Host, and every request after setup is
// complete, is refused as before. A rebinding page cannot know the capability
// and so never gets past the redemption page.

// setupPageAssets are the static files the setup page loads.
var setupPageAssets = map[string]bool{
	"/assets/owngit.css":                     true,
	"/assets/owngit.js":                      true,
	"/assets/logo.svg":                       true,
	"/assets/fonts/PretendardVariable.woff2": true,
	"/assets/page-complete.css":              true,
}

// setupHostBinding records the Host of the one setup session redeemed from an
// unknown Host. Only one setup session exists at a time, and it lives in this
// process: after a restart the binding is gone and the unknown Host is refused
// again until the link is redeemed anew.
type setupHostBinding struct {
	mu      sync.Mutex
	session [32]byte
	host    string
}

func (binding *setupHostBinding) set(sessionToken, host string) {
	binding.mu.Lock()
	defer binding.mu.Unlock()
	binding.session, binding.host = sha256.Sum256([]byte(sessionToken)), host
}

func (binding *setupHostBinding) clear() {
	binding.mu.Lock()
	defer binding.mu.Unlock()
	binding.session, binding.host = [32]byte{}, ""
}

func (binding *setupHostBinding) matches(sessionToken, host string) bool {
	binding.mu.Lock()
	defer binding.mu.Unlock()
	return binding.host != "" && binding.host == host && binding.session == sha256.Sum256([]byte(sessionToken))
}

// unknownHost reports whether the policy refuses the request's Host, and
// returns that Host normalized, or "" when it is not a valid name.
func (app *App) unknownHost(request *http.Request) (string, bool) {
	info := requestctx.Of(request)
	if app.Hosts.Allows(info.Host, info.Peer) {
		return "", false
	}
	host, _ := NormalizeHost(info.Host)
	return host, true
}

// admitUnknownHost decides whether a request from a Host the policy refuses
// may still reach setup. See the comment at the top of this file. An error
// means the state that decides it could not be read.
func (app *App) admitUnknownHost(request *http.Request) (bool, error) {
	if host, unknown := app.unknownHost(request); !unknown || host == "" {
		return false, nil
	}
	path, method := request.URL.Path, request.Method
	switch {
	case setupPageAssets[path] && (method == http.MethodGet || method == http.MethodHead):
	case path == "/setup" && method == http.MethodGet:
	case path == "/setup/redeem" && method == http.MethodPost:
	case path == "/setup" && method == http.MethodPost:
	case isSetupFolderPath(path) && method == http.MethodPost:
	default:
		return false, nil
	}
	// After setup every such request is refused, whatever its session.
	settings, err := app.Store.Settings(request.Context())
	if err != nil || settings.Initialized {
		return false, err
	}
	if (path == "/setup" || isSetupFolderPath(path)) && method == http.MethodPost {
		_, ok, err := app.setupSessionForHost(request)
		return ok, err
	}
	return true, nil
}

// setupSessionForHost returns the setup session of this request, with the
// outcomes of cookieSession. From an unknown Host the session must be the one
// bound to that Host.
func (app *App) setupSessionForHost(request *http.Request) (state.Session, bool, error) {
	session, ok, err := app.setupSession(request)
	if err != nil || !ok {
		return state.Session{}, false, err
	}
	if host, unknown := app.unknownHost(request); unknown {
		cookie, err := request.Cookie(cookieNameForScheme(request, setupCookie))
		if err != nil || !app.setupHosts.matches(cookie.Value, host) {
			return state.Session{}, false, nil
		}
	}
	return session, true, nil
}
