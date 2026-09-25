package requestctx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestParseTrustedProxy(t *testing.T) {
	for value, want := range map[string]string{
		"192.0.2.10":        "192.0.2.10",
		" 192.0.2.10 ":      "192.0.2.10",
		"192.0.2.10/32":     "192.0.2.10",
		"172.18.0.0/16":     "172.18.0.0/16",
		"10.0.0.0/8":        "10.0.0.0/8",
		"127.0.0.1":         "127.0.0.1",
		"::ffff:192.0.2.10": "192.0.2.10",
		"FD00:AB::/32":      "fd00:ab::/32",
		"fd00:ab:cd::/48":   "fd00:ab:cd::/48",
		"2001:DB8::ABCD":    "2001:db8::abcd",
		"2001:db8::1/128":   "2001:db8::1",
		"::1":               "::1",
	} {
		prefix, err := ParseTrustedProxy(value)
		if err != nil {
			t.Errorf("%q refused: %v", value, err)
			continue
		}
		if got := FormatTrustedProxy(prefix); got != want {
			t.Errorf("%q became %q, want %q", value, got, want)
		}
	}
	for value, reason := range map[string]string{
		"0.0.0.0/0":             "too many addresses",
		"::/0":                  "too many addresses",
		"0.0.0.0/1":             "too many addresses",
		"128.0.0.0/1":           "too many addresses",
		"::/1":                  "too many addresses",
		"8000::/1":              "too many addresses",
		"2000::/3":              "too many addresses",
		"224.0.0.0/4":           "too many addresses",
		"fd00::/8":              "too many addresses",
		"2001:db8::/31":         "too many addresses",
		"0.0.0.0/32":            "unspecified",
		"0.0.0.0/8":             "unspecified",
		"::/128":                "unspecified",
		"::/64":                 "unspecified",
		"::ffff:0.0.0.0/96":     "IPv4-mapped",
		"::ffff:192.0.2.0/120":  "IPv4-mapped",
		"0.0.0.0":               "unspecified",
		"::":                    "unspecified",
		"192.168.1.5/24":        "bits set",
		"fe80::1%en0":           "zone",
		"proxy.internal":        "host names",
		"":                      "IP address",
		"192.0.2.10:8080":       "IP address",
		"192.0.2.0/33":          "CIDR",
		"*":                     "IP address",
		"192.0.2.10, 192.0.2.1": "IP address",
	} {
		if _, err := ParseTrustedProxy(value); err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("%q: err=%v, want a refusal naming %q", value, err, reason)
		}
	}
}

// proxyRequest is a request from peer with the given header lines.
func proxyRequest(peer string, headers [][2]string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "127.0.0.1:7654"
	request.RemoteAddr = peer
	for _, header := range headers {
		request.Header.Add(header[0], header[1])
	}
	return request
}

func trustingResolver(t *testing.T, proxies ...string) Resolver {
	t.Helper()
	resolver := Resolver{HostAllowed: func(host string) bool {
		return host == "gitbox.test" || host == "gitbox.test:8443" || host == "127.0.0.1:7654"
	}}
	for _, value := range proxies {
		prefix, err := ParseTrustedProxy(value)
		if err != nil {
			t.Fatal(err)
		}
		resolver.TrustedProxies = append(resolver.TrustedProxies, prefix)
	}
	return resolver
}

func TestForwardedHeadersFromUntrustedPeersChangeNothing(t *testing.T) {
	resolver := trustingResolver(t, "192.0.2.10", "10.1.0.0/16", "fd00::10")
	headers := [][2]string{
		{"X-Forwarded-For", "203.0.113.9"}, {"X-Forwarded-Proto", "https"}, {"X-Forwarded-Host", "gitbox.test"},
		{"Forwarded", "for=203.0.113.9;proto=https;host=gitbox.test"}, {"X-Real-IP", "203.0.113.9"},
	}
	for _, peer := range []string{
		"192.0.2.11:5000", "10.2.0.1:5000", "[fd00::11]:5000", "127.0.0.1:5000", "[::1]:5000",
		"192.168.1.20:5000", "unix-socket", "", "[fd00::10%en0", "192.0.2.10:notaport:1",
	} {
		request := proxyRequest(peer, headers)
		if got, want := resolver.Resolve(request), direct(request); got != want {
			t.Errorf("peer %q: %#v, want the connection's own values %#v", peer, got, want)
		}
	}
}

func TestForwardedHeadersFromTrustedProxies(t *testing.T) {
	resolver := trustingResolver(t, "192.0.2.10", "10.1.0.0/16", "fd00::10")
	for _, test := range []struct {
		name    string
		peer    string
		headers [][2]string
		want    Info
	}{
		{"all three headers", "192.0.2.10:5000",
			[][2]string{{"X-Forwarded-For", "203.0.113.9"}, {"X-Forwarded-Proto", "https"}, {"X-Forwarded-Host", "gitbox.test"}},
			Info{"https", "gitbox.test", "203.0.113.9", "192.0.2.10:5000"}},
		{"proxy in a trusted range", "10.1.200.3:5000",
			[][2]string{{"X-Forwarded-For", "198.51.100.4"}, {"X-Forwarded-Proto", "https"}},
			Info{"https", "127.0.0.1:7654", "198.51.100.4", "10.1.200.3:5000"}},
		{"IPv6 proxy with a zone", "[fd00::10%en0]:5000",
			[][2]string{{"X-Forwarded-For", "2001:db8::7"}, {"X-Forwarded-Proto", "http"}},
			Info{"http", "127.0.0.1:7654", "2001:db8::7", "[fd00::10%en0]:5000"}},
		{"IPv4-mapped peer", "[::ffff:192.0.2.10]:5000",
			[][2]string{{"X-Forwarded-For", "203.0.113.9"}},
			Info{"http", "127.0.0.1:7654", "203.0.113.9", "[::ffff:192.0.2.10]:5000"}},
		{"peer without a port", "192.0.2.10",
			[][2]string{{"X-Forwarded-Proto", "https"}},
			Info{"https", "127.0.0.1:7654", "192.0.2.10", "192.0.2.10"}},
		{"client-supplied entries left of the proxy's are ignored", "192.0.2.10:5000",
			[][2]string{{"X-Forwarded-For", "127.0.0.1, 10.0.0.1, 203.0.113.9"}},
			Info{"http", "127.0.0.1:7654", "203.0.113.9", "192.0.2.10:5000"}},
		{"repeated header lines form one list", "192.0.2.10:5000",
			[][2]string{{"X-Forwarded-For", "127.0.0.1"}, {"X-Forwarded-For", "203.0.113.9"}},
			Info{"http", "127.0.0.1:7654", "203.0.113.9", "192.0.2.10:5000"}},
		{"forwarded address in IPv4-mapped form", "192.0.2.10:5000",
			[][2]string{{"X-Forwarded-For", "::ffff:203.0.113.9"}},
			Info{"http", "127.0.0.1:7654", "203.0.113.9", "192.0.2.10:5000"}},
		{"Host with a port", "192.0.2.10:5000",
			[][2]string{{"X-Forwarded-Host", "gitbox.test:8443"}, {"X-Forwarded-Proto", "https"}},
			Info{"https", "gitbox.test:8443", "192.0.2.10", "192.0.2.10:5000"}},
		{"no forwarded headers", "192.0.2.10:5000", nil,
			Info{"http", "127.0.0.1:7654", "192.0.2.10", "192.0.2.10:5000"}},
	} {
		if got := resolver.Resolve(proxyRequest(test.peer, test.headers)); got != test.want {
			t.Errorf("%s: %#v, want %#v", test.name, got, test.want)
		}
	}
}

// Each malformed or ambiguous header leaves the value derived from the
// connection, while the other headers still apply.
func TestMalformedForwardedHeadersFallBackToTheConnection(t *testing.T) {
	resolver := trustingResolver(t, "192.0.2.10")
	const peer = "192.0.2.10:5000"
	for _, proto := range [][]string{
		{"https", "https"}, {"https, http"}, {"https,https"}, {"HTTPS"}, {"Https"}, {"ftp"}, {""}, {"https;"}, {"wss"},
	} {
		headers := [][2]string{{"X-Forwarded-For", "203.0.113.9"}}
		for _, value := range proto {
			headers = append(headers, [2]string{"X-Forwarded-Proto", value})
		}
		info := resolver.Resolve(proxyRequest(peer, headers))
		if info.Scheme != "http" || info.ClientAddress != "203.0.113.9" {
			t.Errorf("X-Forwarded-Proto %q: scheme=%q client=%q, want http and the forwarded client", proto, info.Scheme, info.ClientAddress)
		}
	}
	for _, forwardedFor := range [][]string{
		{"unknown"}, {""}, {"203.0.113.9,"}, {"203.0.113.9, "}, {"203.0.113.9:4000"}, {"[2001:db8::7]"},
		{"203.0.113.9, garbage"}, {"203.0.113.9", "not-an-address"}, {"_hidden"}, {"203.0.113.9 203.0.113.10"},
	} {
		headers := [][2]string{{"X-Forwarded-Proto", "https"}}
		for _, value := range forwardedFor {
			headers = append(headers, [2]string{"X-Forwarded-For", value})
		}
		info := resolver.Resolve(proxyRequest(peer, headers))
		if info.ClientAddress != "192.0.2.10" || info.Scheme != "https" {
			t.Errorf("X-Forwarded-For %q: client=%q scheme=%q, want the proxy's own address and https", forwardedFor, info.ClientAddress, info.Scheme)
		}
	}
	for _, forwardedHost := range [][]string{
		{"attacker.example"}, {"gitbox.test", "gitbox.test"}, {"gitbox.test, gitbox.test"}, {"gitbox.test,attacker.example"}, {""},
	} {
		headers := [][2]string{{"X-Forwarded-Proto", "https"}}
		for _, value := range forwardedHost {
			headers = append(headers, [2]string{"X-Forwarded-Host", value})
		}
		info := resolver.Resolve(proxyRequest(peer, headers))
		if info.Host != "127.0.0.1:7654" || info.Scheme != "https" {
			t.Errorf("X-Forwarded-Host %q: host=%q scheme=%q, want the request Host and https", forwardedHost, info.Host, info.Scheme)
		}
	}
	// A forwarded Host never replaces a raw Host that the Host check refuses,
	// so it cannot widen that check.
	for _, forwardedHost := range []string{"gitbox.test", "127.0.0.1:7654"} {
		request := proxyRequest(peer, [][2]string{{"X-Forwarded-Host", forwardedHost}})
		request.Host = "rebind.attacker.example"
		if info := resolver.Resolve(request); info.Host != "rebind.attacker.example" {
			t.Errorf("raw Host refused, X-Forwarded-Host %q: host=%q, want the raw Host", forwardedHost, info.Host)
		}
	}
	// Without a Host check the header is never used.
	noHostCheck := Resolver{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}}
	if info := noHostCheck.Resolve(proxyRequest(peer, [][2]string{{"X-Forwarded-Host", "gitbox.test"}})); info.Host != "127.0.0.1:7654" {
		t.Errorf("X-Forwarded-Host was used without a Host check: %q", info.Host)
	}
	// RFC 7239 Forwarded is ignored even from a trusted proxy.
	forwarded := proxyRequest(peer, [][2]string{{"Forwarded", `for=203.0.113.9;proto=https;host=gitbox.test`}})
	if got, want := resolver.Resolve(forwarded), direct(forwarded); got != want {
		t.Errorf("Forwarded header applied: %#v, want %#v", got, want)
	}
}

func TestMiddlewareAttachesTheProxyView(t *testing.T) {
	resolver := trustingResolver(t, "192.0.2.10")
	request := proxyRequest("192.0.2.10:5000", [][2]string{{"X-Forwarded-For", "203.0.113.9"}, {"X-Forwarded-Proto", "https"}})
	var attached Info
	resolver.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, inner *http.Request) {
		attached = Of(inner)
	})).ServeHTTP(httptest.NewRecorder(), request)
	if want := (Info{"https", "127.0.0.1:7654", "203.0.113.9", "192.0.2.10:5000"}); attached != want {
		t.Fatalf("attached %#v, want %#v", attached, want)
	}
	// A request that skipped the middleware never trusts forwarded headers.
	if fallback := Of(request); fallback != direct(request) {
		t.Fatalf("fallback %#v trusted a forwarded header", fallback)
	}
}
