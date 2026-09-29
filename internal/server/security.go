package server

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"

	"owngit/internal/requestctx"
	"owngit/internal/webui"
)

type HostPolicy struct {
	mu      sync.RWMutex
	allowed map[string]struct{}
	// signInRequired is set only in the OwnGit container image; see
	// InContainer.
	signInRequired func() (bool, int64, error)
}

// InContainer makes the policy follow the rule of the OwnGit container
// image, where a connection's peer address cannot tell the computer that
// runs the container from another device: Docker's proxy for IPv6 and
// rootless Docker forward every client from an address inside the
// container network. There a loopback name is accepted from a peer that is
// not a loopback address only while signInRequired reports that access
// needs a password. The Host check then only has to stop DNS rebinding,
// which never produces a loopback name, and every peer still meets the
// sign-in. While access is open, and before setup, such a request is
// refused, as outside the image. signInRequired must not be nil; call
// InContainer before the policy serves requests.
func (policy *HostPolicy) InContainer(signInRequired func() (bool, int64, error)) {
	policy.signInRequired = signInRequired
}

func NewHostPolicy(hosts ...string) *HostPolicy {
	policy := &HostPolicy{allowed: make(map[string]struct{})}
	for _, host := range append([]string{"localhost", "127.0.0.1", "::1"}, hosts...) {
		_ = policy.Add(host)
	}
	return policy
}

func (policy *HostPolicy) Add(value string) error {
	host, err := normalizeHost(value)
	if err != nil {
		return err
	}
	policy.mu.Lock()
	policy.allowed[host] = struct{}{}
	policy.mu.Unlock()
	return nil
}

// Remove stops accepting a Host name. The loopback names stay accepted.
func (policy *HostPolicy) Remove(value string) {
	host, err := normalizeHost(value)
	if err != nil || host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return
	}
	policy.mu.Lock()
	delete(policy.allowed, host)
	policy.mu.Unlock()
}

// Allows reports whether the policy accepts requestHost on a connection from
// peer, the raw address of the connection's other end (requestctx.Info.Peer,
// never a forwarded client address). A name that points at this computer,
// such as localhost, 127.0.0.1 or ::1, is accepted only when peer is a
// loopback address: any device can send it as Host, and only this computer's
// own connections, including a proxy or Tailscale Serve running here, use it.
// In the container image it is also accepted from other peers while access
// needs a password (see InContainer); when that cannot be read, it is not.
func (policy *HostPolicy) Allows(requestHost, peer string) bool {
	allowed, _ := policy.allow(requestHost, peer)
	return allowed
}

type containerAdmissionKey struct{}

// containerAdmissionVersion is set only for a remote loopback Host admitted
// because shared-password access was enabled at the Host check.
func containerAdmissionVersion(ctx context.Context) (int64, bool) {
	version, ok := ctx.Value(containerAdmissionKey{}).(int64)
	return version, ok
}

func (policy *HostPolicy) allow(requestHost, peer string) (bool, *int64) {
	host, err := normalizeHost(requestHost)
	if err != nil {
		return false, nil
	}
	var admission *int64
	if loopbackName(host) && !loopbackPeer(peer) {
		if policy.signInRequired == nil {
			return false, nil
		}
		required, version, err := policy.signInRequired()
		if err != nil || !required {
			return false, nil
		}
		admission = &version
	}
	policy.mu.RLock()
	_, allowed := policy.allowed[host]
	policy.mu.RUnlock()
	return allowed, admission
}

func (policy *HostPolicy) Middleware(next http.Handler) http.Handler {
	return policy.MiddlewareAdmitting(nil, nil, next)
}

// MiddlewareAdmitting is Middleware with one exception: a request whose Host
// the policy refuses still passes when admit, if not nil, accepts it. When
// admit could not decide, unavailable answers the request with the cause:
// the Host may be admitted, so it is not refused. The Origin check and the
// security headers apply either way.
func (policy *HostPolicy) MiddlewareAdmitting(admit func(*http.Request) (bool, error), unavailable func(http.ResponseWriter, *http.Request, string, error), next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		info := requestctx.Of(request)
		allowed, admission := policy.allow(info.Host, info.Peer)
		if !allowed {
			admitted, err := false, error(nil)
			if admit != nil {
				admitted, err = admit(request)
			}
			if err != nil {
				unavailable(writer, request, "setup Host admission", err)
				return
			}
			if !admitted {
				refuseHost(writer, request)
				return
			}
		}
		if admission != nil {
			request = request.WithContext(context.WithValue(request.Context(), containerAdmissionKey{}, *admission))
		}
		if origin := request.Header.Get("Origin"); origin != "" && !sameOrigin(request, origin) {
			if strings.HasPrefix(request.URL.Path, "/api/") {
				writeAPIError(writer, http.StatusForbidden, "origin_mismatch", "The supplied Origin does not match this server.", nil)
			} else {
				http.Error(writer, "origin does not match this server", http.StatusForbidden)
			}
			return
		}
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "same-origin")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(writer, request)
	})
}

func refuseHost(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/api/") {
		writeAPIError(writer, http.StatusMisdirectedRequest, "unrecognized_host", "The request Host is not approved.", nil)
	} else {
		http.Error(writer, "unrecognized host\n"+webui.Text(refusedHostLang(request), webui.MsgHostRefusedHint), http.StatusMisdirectedRequest)
	}
}

// refusedHostLang is the language of the refused-Host page: the saved
// language choice for this address, if any, otherwise the default.
func refusedHostLang(request *http.Request) webui.Lang {
	if cookie, err := request.Cookie(languageCookie); err == nil {
		if lang, valid := webui.ParseLang(cookie.Value); valid {
			return lang
		}
	}
	return webui.DefaultLang
}

// loopbackName reports whether a normalized Host names this computer itself:
// localhost, a name under localhost, or a loopback IP address. normalizeHost
// has already turned an IPv4-mapped IPv6 address into its IPv4 form.
func loopbackName(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.Unmap().IsLoopback()
}

// loopbackPeer reports whether a connection's raw peer address, with or
// without a port, is a loopback address, IPv4-mapped IPv6 included.
func loopbackPeer(peer string) bool {
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = host
	}
	address, err := netip.ParseAddr(peer)
	return err == nil && address.Unmap().IsLoopback()
}

// refuseFunnel refuses every request that carries Tailscale's Funnel header.
// Tailscale sets it on requests from the public Internet through Funnel,
// which OwnGit never enables and must never answer, whoever configured it.
// Nothing else reads Tailscale's headers: the Tailscale-User-* identity
// headers grant nothing, since OwnGit has its own passwords.
func refuseFunnel(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if _, present := request.Header["Tailscale-Funnel-Request"]; present {
			if strings.HasPrefix(request.URL.Path, "/api/") {
				writeAPIError(writer, http.StatusForbidden, "funnel_refused", "OwnGit does not answer requests from the public Internet through Tailscale Funnel.", nil)
			} else {
				http.Error(writer, "OwnGit does not answer requests from the public Internet through Tailscale Funnel", http.StatusForbidden)
			}
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func requireFormOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	return origin != "" && sameOrigin(request, origin)
}

func sameOrigin(request *http.Request, value string) bool {
	origin, err := url.Parse(value)
	if err != nil || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	info := requestctx.Of(request)
	return origin.Scheme == info.Scheme && strings.EqualFold(origin.Host, info.Host)
}

func normalizeHost(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.TrimSuffix(strings.Trim(value, "[]"), ".")
	if value == "" || strings.ContainsAny(value, "/\\@\x00\r\n \t") {
		return "", &net.AddrError{Err: "invalid host", Addr: value}
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String(), nil
	}
	if len(value) > 253 {
		return "", &net.AddrError{Err: "host is too long", Addr: value}
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", &net.AddrError{Err: "invalid host", Addr: value}
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return "", &net.AddrError{Err: "invalid host", Addr: value}
		}
	}
	return value, nil
}
