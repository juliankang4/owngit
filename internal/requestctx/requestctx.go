// Package requestctx derives, in one place, what the server believes about
// the origin of an HTTP request: the scheme and Host the client addressed,
// the client address that password lockouts and setup warnings use, and the
// raw address of the connection's peer.
//
// Every handler reads these values through Of instead of request.TLS,
// request.Host or request.RemoteAddr. Today the values come only from the
// connection itself; forwarded headers such as X-Forwarded-For and
// X-Forwarded-Proto are ignored whoever sends them. Trust in a reverse proxy
// belongs in Resolver, so it applies to every caller at once.
package requestctx

import (
	"context"
	"net"
	"net/http"
)

// Info is the effective origin of one request.
type Info struct {
	// Scheme is "https" when the request arrived over TLS, otherwise "http".
	Scheme string
	// Host is the Host the client addressed, including any port.
	Host string
	// ClientAddress identifies the client for password lockouts and setup
	// approval warnings: the host part of Peer, or Peer itself when it has
	// no port.
	ClientAddress string
	// Peer is the raw network address of the connection's other end.
	Peer string
}

// Secure reports whether the client reached the server over HTTPS.
func (info Info) Secure() bool { return info.Scheme == "https" }

// Origin is the scheme and Host the client addressed, such as
// "http://127.0.0.1:7654".
func (info Info) Origin() string { return info.Scheme + "://" + info.Host }

// Resolver derives Info from a request. The zero value trusts only the
// connection: TLS state, the Host header and the peer address.
type Resolver struct{}

// Resolve derives the Info of request.
func (Resolver) Resolve(request *http.Request) Info {
	return direct(request)
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
