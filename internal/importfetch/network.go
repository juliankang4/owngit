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

// ErrPlainHTTP reports an http:// source URL or redirect for a source that
// was not allowed to use plain HTTP.
var ErrPlainHTTP = errors.New("source URL uses plain HTTP; allow plain HTTP for this source to use it")

// ParseSourceURL applies the source URL rules shared by configuration and the
// transport: an absolute HTTPS URL, or HTTP when allowPlainHTTP is set, with
// an ASCII host and no user information, query or fragment. Its messages name
// the rule and never repeat the URL, which could hold a secret.
func ParseSourceURL(raw string, maxBytes int, allowPlainHTTP bool) (*url.URL, error) {
	if raw == "" || len(raw) > maxBytes || strings.ContainsAny(raw, "\x00\r\n") {
		return nil, errors.New("source URL is empty, too long, or contains control bytes")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.Opaque != "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("source URL must be an absolute HTTPS URL without user information, query, or fragment")
	}
	if parsed.Scheme == "http" && !allowPlainHTTP {
		return nil, ErrPlainHTTP
	}
	if err := checkHost(parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// ParseRedirectOrigin checks an approved redirect origin, such as
// https://mirror.example, and returns it in canonical form: lowercase host and
// no default port. An http origin needs allowPlainHTTP, like a source URL.
func ParseRedirectOrigin(raw string, allowPlainHTTP bool) (string, error) {
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\x00\r\n") {
		return "", errors.New("approved redirect origin is empty, too long, or contains control bytes")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.Opaque != "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("approved redirect origin must be a scheme and host such as https://mirror.example, without a path")
	}
	if parsed.Scheme == "http" && !allowPlainHTTP {
		return "", errors.New("approved redirect origin uses plain HTTP; allow plain HTTP for this source to use it")
	}
	if err := checkHost(parsed); err != nil {
		return "", err
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" && port != defaultPort(parsed.Scheme) {
		host += ":" + port
	}
	return parsed.Scheme + "://" + host, nil
}

func checkHost(parsed *url.URL) error {
	if strings.ContainsAny(parsed.Host, "\x00\r\n ") || parsed.Hostname() == "" {
		return errors.New("source URL host is invalid")
	}
	for _, character := range parsed.Hostname() {
		if character > 127 {
			return errors.New("source URL host must be ASCII (use its ASCII encoding)")
		}
	}
	if strings.Contains(parsed.Hostname(), "%") {
		// Scoped IPv6 destinations are link-local in normal use and do not give
		// TLS an unambiguous source identity.
		return errors.New("source URL host must not contain an IPv6 zone identifier")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return errors.New("source URL port is invalid")
		}
	}
	return nil
}

func defaultPort(scheme string) string {
	if scheme == "http" {
		return "80"
	}
	return "443"
}

// originKey identifies the scheme, host and port that a URL reaches, with
// the default port written out, so equal origins compare equal.
func originKey(target *url.URL) string {
	port := target.Port()
	if port == "" {
		port = defaultPort(target.Scheme)
	}
	return target.Scheme + "://" + net.JoinHostPort(strings.ToLower(target.Hostname()), port)
}

func parseSource(raw string, maxBytes int, allowPlainHTTP bool) (*url.URL, error) {
	parsed, err := ParseSourceURL(raw, maxBytes, allowPlainHTTP)
	if err != nil {
		return nil, fetchError("validate source", ErrInvalidRequest, err)
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

// addressPolicy is what one source may reach beyond public unicast
// addresses. Each consent is separate: an exceptional destination does not
// make a private address reachable, nor the reverse.
type addressPolicy struct {
	allowPrivate  bool
	allowReserved bool
}

func resolveSource(ctx context.Context, base *url.URL, policy addressPolicy, lookup resolver) (*source, error) {
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
		if err := policy.check(address); err != nil {
			return nil, err
		}
	}
	port := base.Port()
	if port == "" {
		port = defaultPort(base.Scheme)
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

// Consents an address can require, as reported in Error.Consent.
const (
	ConsentPrivateNetwork         = "private_network"
	ConsentExceptionalDestination = "exceptional_destination"
)

type destinationKind int

const (
	destinationPublic destinationKind = iota
	// destinationPrivate needs private-network consent.
	destinationPrivate
	// destinationReserved is usable special-purpose unicast. It needs this
	// source's exceptional-destination consent.
	destinationReserved
	// destinationRefused is never reachable: malformed, unspecified,
	// multicast, scoped, deprecated or not a destination at all.
	destinationRefused
)

type specialRange struct {
	prefix netip.Prefix
	name   string
	kind   destinationKind
}

func special(prefix, name string, kind destinationKind) specialRange {
	return specialRange{prefix: netip.MustParsePrefix(prefix), name: name, kind: kind}
}

// specialRanges are the special-purpose ranges of the IANA IPv4 and IPv6
// registries that are not public destinations.
var specialRanges = []specialRange{
	special("0.0.0.0/8", "this-network range", destinationRefused),
	special("10.0.0.0/8", "private range", destinationPrivate),
	special("100.64.0.0/10", "shared address range", destinationPrivate),
	special("127.0.0.0/8", "loopback range", destinationPrivate),
	// Link-local addresses include cloud metadata services such as
	// 169.254.169.254, which no consent reaches.
	special("169.254.0.0/16", "link-local range", destinationRefused),
	special("172.16.0.0/12", "private range", destinationPrivate),
	special("192.0.0.0/24", "IETF protocol assignment range", destinationReserved),
	special("192.0.2.0/24", "documentation range", destinationReserved),
	special("192.88.99.0/24", "6to4 relay range", destinationReserved),
	special("192.168.0.0/16", "private range", destinationPrivate),
	special("198.18.0.0/15", "benchmarking range", destinationReserved),
	special("198.51.100.0/24", "documentation range", destinationReserved),
	special("203.0.113.0/24", "documentation range", destinationReserved),
	special("224.0.0.0/4", "multicast range", destinationRefused),
	special("240.0.0.0/4", "reserved range", destinationRefused),
	special("::1/128", "loopback address", destinationPrivate),
	// IPv4-compatible IPv6 addresses are deprecated, and their embedded IPv4
	// bits must not bypass classification. The loopback above comes first.
	special("::/96", "deprecated IPv4-compatible range", destinationRefused),
	special("64:ff9b::/96", "NAT64 range", destinationReserved),
	// A local-use NAT64 prefix has no fixed place for the IPv4 address it
	// reaches, so OwnGit cannot check that address and refuses the range.
	special("64:ff9b:1::/48", "local NAT64 range", destinationRefused),
	special("100::/64", "discard-only range", destinationReserved),
	special("2001::/32", "Teredo range", destinationReserved),
	special("2001:2::/48", "benchmarking range", destinationReserved),
	special("2001:10::/28", "ORCHID range", destinationRefused),
	special("2001:20::/28", "ORCHIDv2 range", destinationRefused),
	special("2001:db8::/32", "documentation range", destinationReserved),
	special("2002::/16", "6to4 range", destinationReserved),
	special("3fff::/20", "documentation range", destinationReserved),
	special("5f00::/16", "segment routing range", destinationReserved),
	special("fc00::/7", "unique local range", destinationPrivate),
	special("fe80::/10", "link-local range", destinationRefused),
	special("fec0::/10", "deprecated site-local range", destinationRefused),
	special("ff00::/8", "multicast range", destinationRefused),
}

var nat64Prefix = netip.MustParsePrefix("64:ff9b::/96")

// classifyAddress names the range an address belongs to and what reaching
// it takes. An address outside every special range is public when it is
// global unicast and refused otherwise.
func classifyAddress(address netip.Addr) (destinationKind, string) {
	if !address.IsValid() || address.Zone() != "" {
		return destinationRefused, "scoped or invalid address"
	}
	address = address.Unmap()
	if address.IsUnspecified() {
		return destinationRefused, "unspecified address"
	}
	for _, candidate := range specialRanges {
		if candidate.prefix.Contains(address) {
			return candidate.kind, candidate.name + " " + candidate.prefix.String()
		}
	}
	if !address.IsGlobalUnicast() {
		return destinationRefused, "non-unicast address"
	}
	return destinationPublic, ""
}

// check refuses an address this policy does not reach, naming the address,
// its range and the consent that would allow it.
func (p addressPolicy) check(address netip.Addr) error {
	kind, name := classifyAddress(address)
	consent := ""
	allowed := false
	switch kind {
	case destinationPublic:
		allowed = true
	case destinationPrivate:
		allowed, consent = p.allowPrivate, ConsentPrivateNetwork
	case destinationReserved:
		allowed, consent = p.allowReserved, ConsentExceptionalDestination
	}
	if !allowed {
		display := ""
		if address.IsValid() {
			display = address.Unmap().String()
		}
		return &Error{Op: "validate source address", Kind: ErrAddressPolicy, Address: display, AddressRange: name, Consent: consent}
	}
	// A NAT64 address reaches the IPv4 address it embeds, which must be
	// reachable on its own terms too.
	if nat64Prefix.Contains(address.Unmap()) {
		bytes := address.Unmap().As16()
		return p.check(netip.AddrFrom4([4]byte{bytes[12], bytes[13], bytes[14], bytes[15]}))
	}
	return nil
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

// roundTrip sends one request. It calls RoundTrip directly so a Location
// header is never followed by http.Client's redirect machinery; only
// discover follows a redirect, under the source's redirect policy.
func roundTrip(client *http.Client, request *http.Request) (*http.Response, error) {
	response, err := client.Transport.RoundTrip(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fetchError("request", ErrConnection, safeContextCause(request.Context(), err))
	}
	return response, nil
}

// do sends a request whose answer is never a redirect: an upload-pack POST
// is not replayed to another location.
func do(client *http.Client, request *http.Request) (*http.Response, error) {
	response, err := roundTrip(client, request)
	if err != nil {
		return nil, err
	}
	if isRedirect(response.StatusCode) {
		_ = response.Body.Close()
		return nil, fetchError("request", ErrRedirect, ErrRedirectRequest)
	}
	return response, nil
}

func isRedirect(status int) bool {
	return status >= 300 && status < 400
}

// Redirect policies of one source.
const (
	RedirectRefuse     = "refuse"
	RedirectSameOrigin = "same_origin"
	RedirectApproved   = "approved"
)

// maxRedirects bounds the redirects one discovery follows.
const maxRedirects = 5

// Why a redirect was refused, beyond the policy itself. Each is the cause of
// an ErrRedirect error.
var (
	ErrRedirectLoop      = errors.New("source redirects form a loop")
	ErrTooManyRedirects  = errors.New("source redirected too many times")
	ErrRedirectDowngrade = errors.New("source redirected from HTTPS to plain HTTP")
	ErrRedirectTarget    = errors.New("source redirect target is not a Git repository address")
	ErrRedirectRequest   = errors.New("source redirected a request that is never followed")
)

// connector opens pinned connections for one fetch. The source origin keeps
// its own trust anchors and credentials; any other origin a redirect reaches
// gets the system trust anchors and no credentials. Every origin is resolved
// once and checked under the same address policy, and every request to it
// uses the one address selected then.
type connector struct {
	policy         addressPolicy
	allowPlainHTTP bool
	redirects      string
	sourceOrigin   string
	approvedOrigin string
	sourceRoots    *x509.CertPool
	limits         Limits
	lookup         resolver
	clients        map[string]*http.Client
	transports     []*http.Transport
}

func (c *connector) connect(ctx context.Context, base *url.URL) (*http.Client, error) {
	origin := originKey(base)
	if client, exists := c.clients[origin]; exists {
		return client, nil
	}
	resolved, err := resolveSource(ctx, base, c.policy, c.lookup)
	if err != nil {
		return nil, err
	}
	roots := c.sourceRoots
	if origin != c.sourceOrigin {
		if roots, err = rootPool(nil); err != nil {
			return nil, err
		}
	}
	client, transport := newHTTPClient(resolved, roots, c.limits)
	if c.clients == nil {
		c.clients = map[string]*http.Client{}
	}
	c.clients[origin] = client
	c.transports = append(c.transports, transport)
	return client, nil
}

func (c *connector) close() {
	for _, transport := range c.transports {
		transport.CloseIdleConnections()
	}
}

// authenticationFor returns the credentials a request to base may carry:
// the source's own for the source origin, none for any other origin, even
// an approved one.
func (c *connector) authenticationFor(base *url.URL, authentication Authentication) Authentication {
	if originKey(base) != c.sourceOrigin {
		return Authentication{}
	}
	return authentication
}

// redirectBase checks one redirect of the advertisement request and returns
// the repository URL it moves to. Like Git, OwnGit follows a redirect only
// for this first request, and only to an address that still ends with
// info/refs?service=git-upload-pack, whose prefix becomes the repository URL
// for every later request.
func (c *connector) redirectBase(target *url.URL, location string) (*url.URL, error) {
	if location == "" || len(location) > c.limits.MaxURLBytes || strings.ContainsAny(location, "\x00\r\n") {
		return nil, fetchError("follow redirect", ErrRedirect, ErrRedirectTarget)
	}
	next, err := target.Parse(location)
	if err != nil {
		return nil, fetchError("follow redirect", ErrRedirect, ErrRedirectTarget)
	}
	if c.redirects != RedirectSameOrigin && c.redirects != RedirectApproved {
		// Name where the request would have gone, scheme and host only.
		return nil, &Error{Op: "follow redirect", Kind: ErrRedirect, RedirectOrigin: displayOrigin(next)}
	}
	if next.Scheme == "http" && !c.allowPlainHTTP {
		return nil, fetchError("follow redirect", ErrRedirect, ErrRedirectDowngrade)
	}
	if next.Fragment != "" || next.RawQuery != "service=git-upload-pack" || !strings.HasSuffix(next.Path, "/info/refs") ||
		(next.RawPath != "" && !strings.HasSuffix(next.RawPath, "/info/refs")) {
		return nil, fetchError("follow redirect", ErrRedirect, ErrRedirectTarget)
	}
	next.Path = strings.TrimSuffix(next.Path, "/info/refs")
	next.RawPath = strings.TrimSuffix(next.RawPath, "/info/refs")
	next.RawQuery = ""
	base, err := parseSource(next.String(), c.limits.MaxURLBytes, c.allowPlainHTTP)
	if err != nil {
		return nil, fetchError("follow redirect", ErrRedirect, ErrRedirectTarget)
	}
	origin := originKey(base)
	if origin != c.sourceOrigin && (c.redirects != RedirectApproved || origin != c.approvedOrigin) {
		return nil, &Error{Op: "follow redirect", Kind: ErrRedirect, RedirectOrigin: displayOrigin(base)}
	}
	return base, nil
}

// displayOrigin is the canonical scheme and host of a checked URL, for
// naming a redirect destination to the owner.
func displayOrigin(target *url.URL) string {
	origin, err := ParseRedirectOrigin(target.Scheme+"://"+target.Host, true)
	if err != nil {
		return ""
	}
	return origin
}

func contentLengthWithin(response *http.Response, maximum int64) bool {
	return response.ContentLength < 0 || response.ContentLength <= maximum
}
