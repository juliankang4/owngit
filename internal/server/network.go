package server

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// DefaultListenAddress is where serve listens when neither a flag nor a
// saved setting names an address.
const DefaultListenAddress = "127.0.0.1:7654"

// HeadlessListenAddress is saved at the first start of a computer without a
// screen for setup, so the setup link works from another device.
const HeadlessListenAddress = "0.0.0.0:7654"

// ValidateListenAddress checks a listen address to be saved: host:port with a
// port from 1 to 65535, and a host that is empty (every interface), an IP
// address or a host name.
func ValidateListenAddress(value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("listen address %q must be host:port: %w", value, err)
	}
	if !validPort(port) {
		return fmt.Errorf("listen address %q needs a port from 1 to 65535", value)
	}
	if host == "" {
		return nil
	}
	if _, err := NormalizeHost(host); err != nil {
		return fmt.Errorf("listen address %q has an invalid host: %w", value, err)
	}
	return nil
}

// ValidateBaseURL checks a base URL, the origin other devices use: http or
// https with a host, and no path, query, fragment or user information, since
// OwnGit is hosted at the origin root. Its host must be a name the Host check
// can accept, and a port, when given, must be from 1 to 65535. It returns the
// URL in canonical form. The serve flag and the saved setting both use it.
func ValidateBaseURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil ||
		parsed.Opaque != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("base URL must be an HTTP(S) origin without a path, query, credentials, or fragment")
	}
	if _, err := NormalizeHost(parsed.Host); err != nil {
		return "", fmt.Errorf("base URL has an invalid host: %w", err)
	}
	if port := parsed.Port(); strings.HasSuffix(parsed.Host, ":") || port != "" && !validPort(port) {
		return "", fmt.Errorf("base URL %q needs a port from 1 to 65535, or none", value)
	}
	return parsed.String(), nil
}

// ValidatePublicShare checks the public share address to be saved: listen,
// the address its listener uses, and publicURL, the origin visitors use to
// reach it through a tunnel or reverse proxy. Both are empty (off) or both
// set. listen must not use the port of privateListen, OwnGit's own listen
// address. It returns publicURL in canonical form.
func ValidatePublicShare(listen, publicURL, privateListen string) (string, error) {
	if listen == "" && publicURL == "" {
		return "", nil
	}
	if listen == "" || publicURL == "" {
		return "", errors.New("the public share address needs both its listen address and the URL visitors use, or neither")
	}
	if err := ValidateListenAddress(listen); err != nil {
		return "", err
	}
	_, port, _ := net.SplitHostPort(listen)
	if _, privatePort, err := net.SplitHostPort(privateListen); err == nil && port == privatePort {
		return "", fmt.Errorf("the public share address needs a port other than %s, which OwnGit itself listens on", port)
	}
	canonical, err := ValidateBaseURL(publicURL)
	if err != nil {
		return "", fmt.Errorf("public share URL: %w", err)
	}
	return canonical, nil
}

// PublicShareWarnings are what a saved public share address means for the
// owner, said by Settings and by "owngit network set": that the Internet
// reaches it, that a plain http: URL sends links and passwords
// unencrypted, that a listen address beyond this computer is reached
// directly, and that visitors look like one address while no proxy is
// trusted. None is said while it is off.
func PublicShareWarnings(listen, publicURL string, trustedProxies []string) []webui.MessageCode {
	if listen == "" {
		return nil
	}
	warnings := []webui.MessageCode{webui.MsgPublicShareWarnOn}
	if strings.HasPrefix(publicURL, "http:") {
		warnings = append(warnings, webui.MsgPublicShareWarnPlainHTTP)
	}
	if host, _, err := net.SplitHostPort(listen); err == nil && !IsLoopbackHost(host) {
		warnings = append(warnings, webui.MsgPublicShareWarnDirect)
	}
	if len(trustedProxies) == 0 {
		warnings = append(warnings, webui.MsgPublicShareWarnProxy)
	}
	return warnings
}

func validPort(port string) bool {
	number, err := strconv.Atoi(port)
	return err == nil && number >= 1 && number <= 65535
}

// NormalizeHost returns the form of a Host name that the Host check compares:
// lowercase, without port, brackets or a trailing dot. It refuses a value
// that is not an IP address or a valid host name.
func NormalizeHost(value string) (string, error) {
	return normalizeHost(value)
}

// IsLoopbackHost reports whether a listen host reaches only this computer.
// An empty host and the unspecified addresses listen on every interface.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// PlainHTTPAddress returns the first address in saved network settings that
// other computers reach over plain HTTP, or "" when there is none: a listen
// address beyond this computer, OwnGit's own or the public share address's,
// or a public share URL that starts with http:.
func PlainHTTPAddress(settings state.NetworkSettings) string {
	for _, listen := range []string{settings.Listen, settings.PublicShareListen} {
		if host, _, err := net.SplitHostPort(listen); err == nil && !IsLoopbackHost(host) {
			return listen
		}
	}
	if strings.HasPrefix(settings.PublicShareURL, "http:") {
		return settings.PublicShareURL
	}
	return ""
}

// PlainHTTPAcknowledgementNeeded reports whether saving settings needs the
// owner's acknowledgement that other computers reach OwnGit over plain
// HTTP: an address does (PlainHTTPAddress) and the acknowledgement was not
// given before (accepted). The Network group of Settings and owngit network
// set both apply it.
func PlainHTTPAcknowledgementNeeded(settings state.NetworkSettings, accepted bool) bool {
	return PlainHTTPAddress(settings) != "" && !accepted
}

// Hosts returns every Host name the policy accepts, sorted.
func (policy *HostPolicy) Hosts() []string {
	policy.mu.RLock()
	hosts := make([]string, 0, len(policy.allowed))
	for host := range policy.allowed {
		hosts = append(hosts, host)
	}
	policy.mu.RUnlock()
	sort.Strings(hosts)
	return hosts
}

// serverOrigin is the address shown to people and tools for reaching this
// server, such as in clone addresses: the configured base URL when serve has
// one (from a flag or the saved setting), otherwise the scheme and Host of
// this request.
func (app *App) serverOrigin(request *http.Request) string {
	if base := app.Network.BaseURL(); base != "" {
		return base
	}
	return requestctx.Of(request).Origin()
}

// cloneURL is the Git address of a repository.
func (app *App) cloneURL(request *http.Request, id string) string {
	return app.serverOrigin(request) + "/git/" + url.PathEscape(id) + ".git"
}

// setupHostToKeep returns the Host this request used when setup may offer to
// keep accepting it after a restart: a name that is not loopback and that the
// next start would not accept from saved settings (the allowed Hosts, the
// saved listen address or the saved base URL). Otherwise it returns "".
func (app *App) setupHostToKeep(request *http.Request) string {
	host, err := NormalizeHost(requestctx.Of(request).Host)
	if err != nil || IsLoopbackHost(host) {
		return ""
	}
	saved, err := app.Store.TrustedHosts(request.Context())
	if err != nil {
		return ""
	}
	network, err := app.Store.NetworkSettings(request.Context())
	if err != nil {
		return ""
	}
	if listenHost, _, err := net.SplitHostPort(network.Listen); err == nil {
		saved = append(saved, listenHost)
	}
	if parsed, err := url.Parse(network.BaseURL); err == nil && parsed.Host != "" {
		saved = append(saved, parsed.Host)
	}
	for _, value := range saved {
		if normalized, err := NormalizeHost(value); err == nil && normalized == host {
			return ""
		}
	}
	return host
}
