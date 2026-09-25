package requestctx

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The derivations below are the ones handlers used before Info existed. Info
// must reproduce them exactly for direct connections.

func legacyScheme(request *http.Request) string {
	if request.TLS != nil {
		return "https"
	}
	return "http"
}

// legacyPeerHost is how the Git CGI environment and the setup approval
// warning took the client address from RemoteAddr.
func legacyPeerHost(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return host
}

// lockoutKey is the key the password lockout derives from the address it is
// given (auth.clientAddress). Handlers used to pass RemoteAddr and now pass
// ClientAddress; the key must not change.
func lockoutKey(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return host
	}
	return address
}

var forwardedHeaders = map[string]string{
	"X-Forwarded-For":   "203.0.113.9",
	"X-Forwarded-Proto": "https",
	"X-Forwarded-Host":  "attacker.example",
	"X-Forwarded-Port":  "443",
	"X-Real-IP":         "203.0.113.10",
	"Forwarded":         `for=203.0.113.11;proto=https;host=attacker.example`,
}

func TestInfoMatchesDirectConnectionDerivation(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		remote string
		tls    bool
		want   Info
	}{
		{"IPv4 plain", "127.0.0.1:7654", "192.0.2.4:51234", false, Info{"http", "127.0.0.1:7654", "192.0.2.4", "192.0.2.4:51234", false}},
		{"IPv4 TLS", "git.example:443", "192.0.2.4:51234", true, Info{"https", "git.example:443", "192.0.2.4", "192.0.2.4:51234", false}},
		{"IPv6 plain", "[::1]:7654", "[::1]:40000", false, Info{"http", "[::1]:7654", "::1", "[::1]:40000", false}},
		{"IPv6 zone TLS", "git.example", "[fe80::1%en0]:40000", true, Info{"https", "git.example", "fe80::1%en0", "[fe80::1%en0]:40000", false}},
		{"Host without port", "localhost", "198.51.100.7:1", false, Info{"http", "localhost", "198.51.100.7", "198.51.100.7:1", false}},
		{"peer without port", "localhost:7654", "192.0.2.4", false, Info{"http", "localhost:7654", "192.0.2.4", "192.0.2.4", false}},
		{"bracketed peer without port", "localhost:7654", "[::1]", false, Info{"http", "localhost:7654", "[::1]", "[::1]", false}},
		{"malformed peer", "localhost:7654", "not an address", false, Info{"http", "localhost:7654", "not an address", "not an address", false}},
		{"empty peer", "localhost:7654", "", false, Info{"http", "localhost:7654", "", "", false}},
	}
	for _, test := range cases {
		for _, forwarded := range []bool{false, true} {
			name := test.name
			if forwarded {
				name += " with forwarded headers"
			}
			t.Run(name, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, "/", nil)
				request.Host = test.host
				request.RemoteAddr = test.remote
				if test.tls {
					request.TLS = &tls.ConnectionState{}
				} else {
					request.TLS = nil
				}
				if forwarded {
					for name, value := range forwardedHeaders {
						request.Header.Set(name, value)
					}
				}
				var attached Info
				Resolver{}.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, inner *http.Request) {
					attached = Of(inner)
				})).ServeHTTP(httptest.NewRecorder(), request)
				for label, info := range map[string]Info{"resolved": (Resolver{}).Resolve(request), "attached": attached, "fallback": Of(request)} {
					if info != test.want {
						t.Fatalf("%s info = %#v, want %#v", label, info, test.want)
					}
					if info.Scheme != legacyScheme(request) || info.Host != request.Host || info.Peer != request.RemoteAddr {
						t.Fatalf("%s info %#v differs from the connection", label, info)
					}
					if info.ClientAddress != legacyPeerHost(request.RemoteAddr) {
						t.Fatalf("%s client address %q, legacy %q", label, info.ClientAddress, legacyPeerHost(request.RemoteAddr))
					}
					if lockoutKey(info.ClientAddress) != lockoutKey(request.RemoteAddr) {
						t.Fatalf("%s lockout key %q, legacy %q", label, lockoutKey(info.ClientAddress), lockoutKey(request.RemoteAddr))
					}
					if info.Secure() != (request.TLS != nil) || info.Origin() != legacyScheme(request)+"://"+request.Host {
						t.Fatalf("%s secure=%v origin=%q", label, info.Secure(), info.Origin())
					}
				}
			})
		}
	}
}
