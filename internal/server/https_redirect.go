package server

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"owngit/internal/requestctx"
)

// A browser that opens a dashboard page over plain HTTP goes to the same
// page at this server's HTTPS address when OwnGit knows that the address
// works and that the browser already uses its name: the browser asked
// OwnGit directly, by the address's host name, on another port that it
// named, such as http://gitbox.tail0000.ts.net:7654/ for
// https://gitbox.tail0000.ts.net. A browser that resolves the name reaches
// the HTTPS address too. One that uses an IP address, localhost or another
// name stays, since it may not reach that name.
//
// The HTTPS address works when it is
//   - Tailscale's address for this server and the sharing report says it is
//     ready: Tailscale has OwnGit's endpoint and the running server accepts
//     it, as read at most tailscaleReadingTTL ago; or
//   - the base URL, and a trusted proxy has passed OwnGit a request for it
//     over HTTPS since this server started (noteHTTPS).
//
// Only pages move (dashboardPage), and only for GET and HEAD without an
// Authorization header, so a form or a password is never sent elsewhere.
// Git, the API, health, assets, setup and downloads are answered where they
// are asked. A request that arrived over HTTPS or from a trusted proxy never
// moves: the proxy decides about its own scheme. A request that came through
// the HTTPS address by a proxy OwnGit does not trust carries that address's
// own Host, which names no other port, so it does not move either, and a
// move never leads back to itself. The target comes only from OwnGit's own
// settings, never from the request. The redirect is temporary and not
// stored, so it ends when the address stops working: at once when sharing
// is turned off or the base URL changes, and within tailscaleReadingTTL
// when Tailscale loses the address.

// httpsRedirectWait bounds how long a page waits for Tailscale's state to
// decide whether it moves. When the wait runs out, the page is answered
// where it was asked, and the reading goes on for the next page.
const httpsRedirectWait = 2 * time.Second

// redirectToHTTPS sends request to the same page at the HTTPS address when
// the rules above allow it, and reports whether it did. When OwnGit cannot
// tell whether the address works, it logs why and answers the page where it
// was asked.
func (app *App) redirectToHTTPS(writer http.ResponseWriter, request *http.Request) bool {
	info := requestctx.Of(request)
	_, password := request.Header["Authorization"]
	if request.Method != http.MethodGet && request.Method != http.MethodHead || !dashboardPage(request.URL.Path) ||
		info.Secure() || info.FromProxy || password {
		return false
	}
	page := localNext(request.URL.RequestURI(), "")
	name, port, named := hostPort(info.Host)
	if page == "" || !named {
		return false
	}
	address, err := app.workingHTTPS(request.Context(), name)
	if err != nil {
		logFailure(request, "HTTPS address check", err)
		return false
	}
	if address == "" {
		return false
	}
	if httpsName, httpsPort, valid := originHostPort(address); !valid || httpsName != name || httpsPort == port {
		return false
	}
	writer.Header().Set("Cache-Control", "no-store")
	http.Redirect(writer, request, address+page, http.StatusTemporaryRedirect)
	return true
}

// workingHTTPS returns the HTTPS address for the host name name that OwnGit
// knows works, or "". For the name Tailscale shares this server under, only
// Tailscale's report decides.
func (app *App) workingHTTPS(ctx context.Context, name string) (string, error) {
	if name == app.Network.TailscaleName() {
		ctx, cancel := context.WithTimeout(ctx, httpsRedirectWait)
		defer cancel()
		report, err := app.Tailscale.Report(ctx)
		if err != nil || !report.Ready {
			return "", err
		}
		return TailscaleOrigin(report.Sharing.Name, report.Sharing.HTTPSPort), nil
	}
	base := app.Network.BaseURL()
	if seen := app.httpsSeen.Load(); seen == nil || *seen != base {
		return "", nil
	}
	return base, nil
}

// noteHTTPS records that the base URL works over HTTPS when request came
// for it as HTTPS. OwnGit itself serves plain HTTP, so only a trusted proxy
// makes a request HTTPS (requestctx).
func (app *App) noteHTTPS(request *http.Request) {
	base := app.Network.BaseURL()
	if !strings.HasPrefix(base, "https://") || !sameOrigin(request, base) {
		return
	}
	if seen := app.httpsSeen.Load(); seen == nil || *seen != base {
		app.httpsSeen.Store(&base)
	}
}

// dashboardPage reports whether path is a page that people open in a
// browser: the overview, activity, sign-in, Settings, and every page of a
// repository except its downloads, raw files and archives, which scripts
// and download tools fetch as well.
func dashboardPage(path string) bool {
	switch {
	case path == "/", path == "/activity", path == "/login", path == "/admin/login", isSettingsPath(path):
		return true
	}
	rest, found := strings.CutPrefix(path, "/repositories/")
	if !found {
		return false
	}
	id, page, _ := strings.Cut(rest, "/")
	return id != "" && page != "raw" && page != "archive"
}

// hostPort returns the normalized name and the port of host, a Host, and
// reports whether host names both, the port explicitly.
func hostPort(host string) (string, int, bool) {
	name, port, err := net.SplitHostPort(host)
	if err != nil || !validPort(port) {
		return "", 0, false
	}
	number, _ := strconv.Atoi(port)
	name, err = NormalizeHost(name)
	return name, number, err == nil
}

// originHostPort is hostPort of an HTTPS origin, whose port is 443 when it
// names none.
func originHostPort(origin string) (string, int, bool) {
	parsed, err := url.Parse(origin)
	if err != nil {
		return "", 0, false
	}
	host := parsed.Host
	if parsed.Port() == "" {
		host = net.JoinHostPort(parsed.Hostname(), "443")
	}
	return hostPort(host)
}
