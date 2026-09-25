package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// The Network block of the Settings page shows the saved network settings,
// what the running server uses, and whether a restart is needed, with the
// same NetworkReport that "owngit network show" prints. Saving writes the
// same settings as "owngit network set", checked by the same functions, and
// never changes the running server. Resetting stays with the host-only
// "owngit network reset", which the block explains.

// networkReport reads the saved settings and this server's running record.
// The page is served by the running server, so the state directory is held;
// the record is trusted only while this process holds the running-record
// lock (RunningRecordLive), by the rule of state.OwnRunningNetwork.
func (app *App) networkReport(ctx context.Context) (NetworkReport, error) {
	saved, err := app.Store.NetworkSettings(ctx)
	if err != nil {
		return NetworkReport{}, err
	}
	hosts, err := app.Store.TrustedHosts(ctx)
	if err != nil {
		return NetworkReport{}, err
	}
	proxies, err := app.Store.TrustedProxies(ctx)
	if err != nil {
		return NetworkReport{}, err
	}
	observed, err := app.Store.OwnRunningNetwork(ctx, app.RunningRecordLive)
	if err != nil {
		return NetworkReport{}, err
	}
	report := NewNetworkReport(saved, hosts, proxies)
	report.SetServer(observed)
	return report, nil
}

// networkRevision names the saved values, so a save from a form opened
// before another change (in another browser or with "owngit network set")
// is refused instead of silently undoing that change.
func networkRevision(report NetworkReport) string {
	hash := sha256.New()
	for _, value := range append([]string{report.Saved.Listen, report.Saved.BaseURL, ""},
		append(append(slices.Clone(report.Saved.AllowedHosts), ""), report.Saved.TrustedProxies...)...) {
		hash.Write([]byte(value))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

// networkInfo turns the report into the Network block.
func networkInfo(report NetworkReport) webui.NetworkInfo {
	_, defaultPort, _ := net.SplitHostPort(DefaultListenAddress)
	info := webui.NetworkInfo{
		Status:        webui.NetworkUnconfirmed,
		Revision:      networkRevision(report),
		DefaultListen: DefaultListenAddress, EveryNetwork: net.JoinHostPort("0.0.0.0", defaultPort),
		ProxyExample: "127.0.0.1, 10.0.0.0/8",
		Form: webui.NetworkForm{
			Listen: report.Saved.Listen, BaseURL: report.Saved.BaseURL,
			Hosts:   strings.Join(report.Saved.AllowedHosts, "\n"),
			Proxies: strings.Join(report.Saved.TrustedProxies, "\n"),
		},
	}
	next := report.NextStart
	info.Listen = webui.NetworkValue{Next: []string{next.Listen}, NextSource: next.ListenSource}
	info.BaseURL = webui.NetworkValue{Next: nonEmpty(next.BaseURL), NextSource: next.BaseURLSource}
	info.Hosts = webui.NetworkValue{Next: report.Saved.AllowedHosts}
	info.Proxies = webui.NetworkValue{Next: next.TrustedProxies, NextSource: next.TrustedProxiesSource}
	if running := report.Running; running != nil {
		info.Status = webui.NetworkCurrent
		if report.RestartNeeded {
			info.Status = webui.NetworkRestart
		}
		pending := report.Pending()
		info.Listen.Now, info.Listen.NowSource, info.Listen.Pending = []string{running.Listen}, running.ListenSource, pending.Listen
		info.BaseURL.Now, info.BaseURL.NowSource, info.BaseURL.Pending = nonEmpty(running.BaseURL), running.BaseURLSource, pending.BaseURL
		info.Hosts.Now, info.Hosts.Pending = runningAllowedHosts(report), pending.Hosts
		info.Proxies.Now, info.Proxies.NowSource, info.Proxies.Pending = running.TrustedProxies, running.TrustedProxiesSource, pending.Proxies
		for _, value := range []*webui.NetworkValue{&info.Listen, &info.BaseURL, &info.Hosts, &info.Proxies} {
			value.NowKnown = true
			info.FromOption = info.FromOption || value.NowSource == NetworkSourceFlag
		}
	}
	host, _, err := net.SplitHostPort(next.Listen)
	info.PlainHTTP = err == nil && !IsLoopbackHost(host)
	info.NeedsName = err == nil && (host == "" || net.ParseIP(host).IsUnspecified()) && next.BaseURL == "" && len(report.Saved.AllowedHosts) == 0
	info.HTTPSWithoutProxy = strings.HasPrefix(next.BaseURL, "https:") && len(next.TrustedProxies) == 0
	return info
}

// runningAllowedHosts lists the extra names the running server accepts: its
// accepted Host names without the loopback names, which are always
// accepted, and without the names of its listen address and base URL, which
// have their own rows, unless such a name is also saved as an allowed name.
func runningAllowedHosts(report NetworkReport) []string {
	running := report.Running
	implied := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	for _, address := range []string{running.Listen, running.Address} {
		if host, _, err := net.SplitHostPort(address); err == nil {
			implied[normalizedOrSelf(host)] = true
		}
	}
	for _, origin := range []string{running.BaseURL, running.Origin} {
		if parsed, err := url.Parse(origin); err == nil && parsed.Host != "" {
			implied[normalizedOrSelf(parsed.Host)] = true
		}
	}
	hosts := []string{}
	for _, host := range running.AcceptedHosts {
		if !implied[host] || slices.Contains(report.Saved.AllowedHosts, host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func normalizedOrSelf(value string) string {
	if host, err := NormalizeHost(value); err == nil {
		return host
	}
	return value
}

func nonEmpty(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

// networkEntries splits a list field into its entries: one per line, and
// commas or spaces also separate entries, since no valid entry contains one.
func networkEntries(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
}

// saveNetwork handles ActionSaveNetwork after the administrator password
// was verified.
func (app *App) saveNetwork(writer http.ResponseWriter, request *http.Request, settings state.Settings, csrf string) {
	ctx := request.Context()
	action := webui.ActionSaveNetwork
	unavailable := func() {
		app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("", webui.MsgErrUnavailable)}, http.StatusServiceUnavailable)
	}
	report, err := app.networkReport(ctx)
	if err != nil {
		unavailable()
		return
	}
	if postValue(request, "network_revision") != networkRevision(report) {
		app.renderSettings(writer, request, settings, csrf, action, []webui.Notice{webui.Error("", webui.MsgNetStale)}, http.StatusConflict)
		return
	}
	form := webui.NetworkForm{
		Listen: strings.TrimSpace(postValue(request, "listen")), BaseURL: strings.TrimSpace(postValue(request, "base_url")),
		Hosts: postValue(request, "allowed_hosts"), Proxies: postValue(request, "trusted_proxies"),
	}
	var notices []webui.Notice
	if form.Listen != "" {
		if err := ValidateListenAddress(form.Listen); err != nil {
			notices = append(notices, webui.Error("listen", webui.MsgNetBadListen))
		}
	}
	baseURL := form.BaseURL
	if baseURL != "" {
		if baseURL, err = ValidateBaseURL(form.BaseURL); err != nil {
			notices = append(notices, webui.Error("base_url", webui.MsgNetBadBaseURL))
		}
	}
	var hosts []string
	for _, value := range networkEntries(form.Hosts) {
		host, err := NormalizeHost(value)
		if err != nil {
			notices = append(notices, webui.Notice{Kind: webui.NoticeError, Code: webui.MsgNetBadHost, Field: "allowed_hosts", Detail: value})
			break
		}
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	var proxies []string
	for _, value := range networkEntries(form.Proxies) {
		prefix, err := requestctx.ParseTrustedProxy(value)
		if err != nil {
			notices = append(notices, webui.Notice{Kind: webui.NoticeError, Code: webui.MsgNetBadProxy, Field: "trusted_proxies", Detail: value})
			break
		}
		if text := requestctx.FormatTrustedProxy(prefix); !slices.Contains(proxies, text) {
			proxies = append(proxies, text)
		}
	}
	// Listening beyond this computer serves other devices over plain HTTP,
	// which needs the same acknowledgement as setup until it was given once.
	acknowledge := false
	if host, _, err := net.SplitHostPort(form.Listen); err == nil && !IsLoopbackHost(host) && !settings.InsecureHTTPAccepted {
		if !formChecked(postValue(request, "insecure_ack")) {
			notices = append(notices, webui.Error("insecure_ack", webui.MsgSetupInsecureNeed))
		}
		acknowledge = true
	}
	if len(notices) > 0 {
		app.renderSettingsPage(writer, request, settings, csrf, action, notices, http.StatusUnprocessableEntity, settingsView{Network: &form, AdminVerified: true})
		return
	}
	update := state.NetworkUpdate{Settings: state.NetworkSettings{Listen: form.Listen, BaseURL: baseURL}}
	stored, err := app.Store.TrustedHosts(ctx)
	if err != nil {
		unavailable()
		return
	}
	// Stored names may predate normalization, so each is compared in the
	// normalized form and removed in its stored spelling.
	kept := map[string]bool{}
	for _, value := range stored {
		host, err := NormalizeHost(value)
		if err != nil || !slices.Contains(hosts, host) {
			update.RemoveHosts = append(update.RemoveHosts, value)
			continue
		}
		kept[host] = true
	}
	for _, host := range hosts {
		if !kept[host] {
			update.AddHosts = append(update.AddHosts, host)
		}
	}
	for _, proxy := range report.Saved.TrustedProxies {
		if !slices.Contains(proxies, proxy) {
			update.RemoveProxies = append(update.RemoveProxies, proxy)
		}
	}
	for _, proxy := range proxies {
		if !slices.Contains(report.Saved.TrustedProxies, proxy) {
			update.AddProxies = append(update.AddProxies, proxy)
		}
	}
	if acknowledge {
		if err := app.Store.AcknowledgeInsecureHTTP(ctx); err != nil {
			unavailable()
			return
		}
	}
	if err := app.Store.UpdateNetwork(ctx, update); err != nil {
		unavailable()
		return
	}
	app.noticeRedirect(writer, request, "/settings?notice=network_saved", http.StatusSeeOther)
}

// networkFocus names the first Network field with an error, in page order.
func networkFocus(notices []webui.Notice) string {
	for _, field := range []string{"listen", "base_url", "allowed_hosts", "trusted_proxies", "insecure_ack", "admin_password"} {
		for _, notice := range notices {
			if notice.Field == field && notice.Kind == webui.NoticeError {
				return field
			}
		}
	}
	return ""
}
