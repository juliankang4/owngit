package importfetch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type source struct {
	base     *url.URL
	host     string
	port     string
	selected netip.Addr
}

func parseSource(raw string, maxBytes int) (*url.URL, error) {
	if raw == "" || len(raw) > maxBytes || strings.ContainsAny(raw, "\x00\r\n") {
		return nil, fetchError("validate source", ErrInvalidRequest, nil)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, fetchError("validate source", ErrInvalidRequest, nil)
	}
	if strings.ContainsAny(parsed.Host, "\x00\r\n ") || parsed.Hostname() == "" {
		return nil, fetchError("validate source", ErrInvalidRequest, nil)
	}
	for _, character := range parsed.Hostname() {
		if character > 127 {
			return nil, fetchError("validate source", ErrInvalidRequest, nil)
		}
	}
	if strings.Contains(parsed.Hostname(), "%") {
		// Scoped IPv6 destinations are link-local in normal use and do not give
		// TLS an unambiguous source identity.
		return nil, fetchError("validate source", ErrInvalidRequest, nil)
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, fetchError("validate source", ErrInvalidRequest, nil)
		}
	}
	return parsed, nil
}

func endpoint(base *url.URL, suffix string, query string) *url.URL {
	target := *base
	separator := "/"
	if strings.HasSuffix(target.Path, "/") {
		separator = ""
	}
	target.Path += separator + suffix
	if target.RawPath != "" {
		target.RawPath += separator + suffix
	}
	target.RawQuery = query
	return &target
}

func resolveSource(ctx context.Context, base *url.URL, allowPrivate bool, lookup resolver) (*source, error) {
	host := base.Hostname()
	var addresses []netip.Addr
	if literal, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{literal}
	} else {
		resolved, err := lookup.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fetchError("resolve source", ErrNameResolution, safeContextCause(ctx, err))
		}
		addresses = resolved
	}
	addresses = uniqueAddresses(addresses)
	if len(addresses) == 0 {
		return nil, fetchError("resolve source", ErrNameResolution, nil)
	}
	// Validate every answer before selecting one. A mixed public/private DNS
	// response is refused without private-network consent instead of silently
	// choosing its public member.
	for _, address := range addresses {
		if !addressAllowed(address, allowPrivate) {
			return nil, fetchError("validate source address", ErrAddressPolicy, nil)
		}
	}
	port := base.Port()
	if port == "" {
		port = "443"
	}
	return &source{base: base, host: host, port: port, selected: addresses[0]}, nil
}

func uniqueAddresses(input []netip.Addr) []netip.Addr {
	seen := make(map[netip.Addr]struct{}, len(input))
	result := make([]netip.Addr, 0, len(input))
	for _, address := range input {
		if !address.IsValid() || address.Zone() != "" {
			// Invalid and scoped values are retained as invalid policy results.
			return []netip.Addr{address}
		}
		address = address.Unmap()
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		result = append(result, address)
	}
	return result
}

var alwaysForbiddenPrefixes = mustPrefixes(
	"0.0.0.0/8",
	"169.254.0.0/16",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"100::/64",
	"2001::/32",
	"2001:2::/48",
	"2001:db8::/32",
	"2001:10::/28",
	"2001:20::/28",
	"2002::/16",
	"3fff::/20",
	"5f00::/16",
	"fec0::/10",
	"fe80::/10",
	"ff00::/8",
)

var (
	sharedAddressPrefix            = netip.MustParsePrefix("100.64.0.0/10")
	deprecatedIPv4CompatiblePrefix = netip.MustParsePrefix("::/96")
)

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}

func addressAllowed(address netip.Addr, allowPrivate bool) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	// IPv4-compatible IPv6 addresses are deprecated and their embedded IPv4
	// bits must not bypass destination classification. IPv4-mapped addresses
	// were normalized above, while the real IPv6 loopback remains an explicit
	// private-consent case below.
	if address != netip.IPv6Loopback() && deprecatedIPv4CompatiblePrefix.Contains(address) {
		return false
	}
	for _, prefix := range alwaysForbiddenPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	if address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() {
		return false
	}
	if address.IsLoopback() || address.IsPrivate() || sharedAddressPrefix.Contains(address) {
		return allowPrivate
	}
	return address.IsGlobalUnicast()
}

func rootPool(bundle []byte) (*x509.CertPool, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fetchError("load TLS roots", ErrInvalidRequest, nil)
	}
	if roots == nil {
		roots = x509.NewCertPool()
	}
	if len(bundle) == 0 {
		return roots, nil
	}
	remaining := bundle
	certificates := 0
	for len(bytes.TrimSpace(remaining)) > 0 {
		remaining = bytes.TrimSpace(remaining)
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return nil, fetchError("parse private CA", ErrInvalidRequest, nil)
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, fetchError("parse private CA", ErrInvalidRequest, nil)
		}
		parsed, err := x509.ParseCertificates(block.Bytes)
		if err != nil || len(parsed) == 0 {
			return nil, fetchError("parse private CA", ErrInvalidRequest, nil)
		}
		for _, certificate := range parsed {
			roots.AddCert(certificate)
			certificates++
		}
		remaining = rest
	}
	if certificates == 0 {
		return nil, fetchError("parse private CA", ErrInvalidRequest, nil)
	}
	return roots, nil
}

type pinnedDialer struct {
	host     string
	port     string
	selected netip.Addr
	dialer   net.Dialer
}

func (d *pinnedDialer) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !strings.EqualFold(host, d.host) || port != d.port {
		return nil, errors.New("refused an unpinned transport destination")
	}
	network = "tcp6"
	if d.selected.Is4() {
		network = "tcp4"
	}
	return d.dialer.DialContext(ctx, network, net.JoinHostPort(d.selected.String(), d.port))
}

func newHTTPClient(source *source, roots *x509.CertPool, limits Limits) (*http.Client, *http.Transport) {
	dialer := &pinnedDialer{
		host:     source.host,
		port:     source.port,
		selected: source.selected,
		dialer: net.Dialer{
			Timeout:   limits.ResponseHeaderTimeout,
			KeepAlive: -1,
		},
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.dialContext,
		ForceAttemptHTTP2:      false,
		TLSNextProto:           make(map[string]func(string, *tls.Conn) http.RoundTripper),
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: source.host},
		TLSHandshakeTimeout:    limits.TLSHandshakeTimeout,
		ResponseHeaderTimeout:  limits.ResponseHeaderTimeout,
		ExpectContinueTimeout:  time.Second,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: limits.MaxHeaderBytes,
		MaxConnsPerHost:        1,
		MaxIdleConns:           0,
		MaxIdleConnsPerHost:    0,
	}
	client := &http.Client{Transport: transport}
	return client, transport
}

func validateResponse(response *http.Response, status int, mediaType string) error {
	if response.StatusCode != status {
		return &Error{Op: "validate response", Kind: ErrHTTPStatus, StatusCode: response.StatusCode}
	}
	if len(response.Trailer) != 0 {
		return fetchError("validate response trailers", ErrResponseHeaders, nil)
	}
	if !identityEncoding(response.Header.Values("Content-Encoding")) {
		return fetchError("validate response encoding", ErrContentEncoding, nil)
	}
	contentTypes := response.Header.Values("Content-Type")
	if len(contentTypes) != 1 || !matchesMediaType(contentTypes[0], mediaType) {
		return fetchError("validate response media type", ErrMediaType, nil)
	}
	return nil
}

func identityEncoding(values []string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			if !strings.EqualFold(strings.TrimSpace(token), "identity") {
				return false
			}
		}
	}
	return true
}

func matchesMediaType(value, expected string) bool {
	if value == "" {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, expected) && len(parameters) == 0
}

func requestHeaderBytes(request *http.Request) int64 {
	bytes := int64(len("Host: ") + len(request.URL.Host) + 2 + 2)
	for name, values := range request.Header {
		for _, value := range values {
			bytes += int64(len(name) + 2 + len(value) + 2)
		}
	}
	if request.ContentLength >= 0 {
		bytes += int64(len("Content-Length: ") + len(strconv.FormatInt(request.ContentLength, 10)) + 2)
	}
	return bytes
}

func fetchError(op string, kind, cause error) error {
	return &Error{Op: op, Kind: kind, cause: cause}
}

func safeContextCause(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}

func do(client *http.Client, request *http.Request) (*http.Response, error) {
	// Call RoundTrip directly so a Location header is never parsed into a
	// destination by http.Client's redirect machinery.
	response, err := client.Transport.RoundTrip(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fetchError("request", ErrConnection, safeContextCause(request.Context(), err))
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		_ = response.Body.Close()
		return nil, fetchError("request", ErrRedirect, nil)
	}
	return response, nil
}

func contentLengthWithin(response *http.Response, maximum int64) bool {
	return response.ContentLength < 0 || response.ContentLength <= maximum
}
