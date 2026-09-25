// Package requestctx derives, in one place, what the server believes about
// the origin of an HTTP request: the scheme and Host the client addressed,
// the client address that password lockouts and setup warnings use, and the
// raw address of the connection's peer.
//
// Every handler reads these values through Of instead of request.TLS,
// request.Host or request.RemoteAddr. By default the values come only from
// the connection itself, and forwarded headers such as X-Forwarded-For and
// X-Forwarded-Proto are ignored whoever sends them. When the owner names a
// reverse proxy as trusted, Resolver believes a few forwarded headers from
// that proxy's connections only (see Resolver.Resolve), so the change applies
// to every caller at once.
package requestctx

import (
	"context"
	"net"
	"net/http"
	"net/netip"
)

// Info is the effective origin of one request.
type Info struct {
	// Scheme is "https" when the request arrived over TLS or, through a
	// trusted proxy, when the proxy says the client used HTTPS; otherwise
	// "http".
	Scheme string
	// Host is the Host the client addressed, including any port.
	Host string
	// ClientAddress identifies the client for password lockouts and setup
	// approval warnings: the host part of Peer, or Peer itself when it has
	// no port, or the client address a trusted proxy forwarded.
	ClientAddress string
	// Peer is the raw network address of the connection's other end, which
	// is the proxy's address for a proxied request.
	Peer string
}

// Secure reports whether the client reached the server over HTTPS.
func (info Info) Secure() bool { return info.Scheme == "https" }

// Origin is the scheme and Host the client addressed, such as
// "http://127.0.0.1:7654".
func (info Info) Origin() string { return info.Scheme + "://" + info.Host }

// Resolver derives Info from a request. The zero value trusts only the
// connection: TLS state, the Host header and the peer address.
type Resolver struct {
	// TrustedProxies are the peers whose forwarded headers are believed.
	// Nothing is trusted by default, not even loopback addresses.
	TrustedProxies []netip.Prefix
	// HostAllowed reports whether the Host check accepts a Host. A trusted
	// proxy's X-Forwarded-Host is used only when it accepts both that value
	// and the request's own Host; nil ignores that header.
	HostAllowed func(host string) bool
}

// Resolve derives the Info of request. When the raw peer is a trusted proxy,
// and only then, it believes these headers from that proxy:
//
//   - X-Forwarded-Proto sets Scheme when it is exactly one value, "http" or
//     "https".
//   - X-Forwarded-For sets ClientAddress to its rightmost entry, the one the
//     proxy appended, when that entry is an IP address. Entries to its left
//     came from the client and are never used.
//   - X-Forwarded-Host sets Host when it is exactly one value and HostAllowed
//     accepts both it and the request's own Host. It can only choose among
//     Hosts that already pass the Host check, never widen that check: a
//     proxy may pass a client's own X-Forwarded-Host through, and a DNS
//     rebinding page can send one.
//
// A missing, repeated, listed or malformed value leaves the value derived
// from the connection. The RFC 7239 Forwarded header is ignored, and Peer is
// always the raw connection address.
func (resolver Resolver) Resolve(request *http.Request) Info {
	info := direct(request)
	if !resolver.trusts(info.Peer) {
		return info
	}
	if proto, ok := singleValue(request.Header, "X-Forwarded-Proto"); ok && (proto == "http" || proto == "https") {
		info.Scheme = proto
	}
	if client, ok := lastForwardedFor(request.Header); ok {
		info.ClientAddress = client
	}
	if host, ok := singleValue(request.Header, "X-Forwarded-Host"); ok && resolver.HostAllowed != nil && resolver.HostAllowed(info.Host) && resolver.HostAllowed(host) {
		info.Host = host
	}
	return info
}

// Middleware resolves each request once and attaches the result, so every
// handler and the Git backend see the same values.
func (resolver Resolver) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		info := resolver.Resolve(request)
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), infoKey{}, info)))
	})
}

// Of returns the Info attached by Middleware. A request that did not pass
// through Middleware, such as one built by a test, gets the values of its
// own connection, which never trust a forwarded header.
func Of(request *http.Request) Info {
	if info, ok := request.Context().Value(infoKey{}).(Info); ok {
		return info
	}
	return direct(request)
}

type infoKey struct{}

// direct derives Info from the connection alone.
func direct(request *http.Request) Info {
	info := Info{Scheme: "http", Host: request.Host, ClientAddress: request.RemoteAddr, Peer: request.RemoteAddr}
	if request.TLS != nil {
		info.Scheme = "https"
	}
	if host, _, err := net.SplitHostPort(request.RemoteAddr); err == nil {
		info.ClientAddress = host
	}
	return info
}
