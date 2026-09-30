package importfetch

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"owngit/internal/importgit"
)

// packSource is a scripted v0 source with one branch and a pack.
func packSource(t *testing.T, authorization string) *scriptedSource {
	return &scriptedSource{
		t:                 t,
		advertisement:     testAdvertisement(testRef(testSHA1A, "refs/heads/main", "ofs-delta")),
		postResponse:      "0008NAK\nPACKpayload",
		wantAuthorization: authorization,
	}
}

// startPlainSource serves source over plain HTTP on the loopback address.
func startPlainSource(t *testing.T, source *scriptedSource) (*httptest.Server, Request) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(source.serveHTTP))
	t.Cleanup(server.Close)
	return server, Request{URL: server.URL + "/group/repo.git", AllowPrivateNetwork: true, AllowPlainHTTP: true}
}

// countingServer counts the requests a redirect target receives.
func countingServer(t *testing.T, tls bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { count.Add(1) })
	server := httptest.NewServer(handler)
	if tls {
		server.Close()
		server = httptest.NewTLSServer(handler)
	}
	t.Cleanup(server.Close)
	return server, &count
}

// redirectDiscovery answers the advertisement request with a redirect to
// location and leaves every other request to the scripted source.
func redirectDiscovery(source *scriptedSource, status int, location string) {
	source.handler = func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/group/repo.git/info/refs" {
			return false
		}
		writer.Header().Set("Location", location)
		writer.WriteHeader(status)
		return true
	}
}

func TestPlainHTTPNeedsTheSourceConsent(t *testing.T) {
	source := packSource(t, "Bearer plain.token")
	_, request := startPlainSource(t, source)
	request.Authentication.BearerToken = "plain.token"

	refused := request
	refused.AllowPlainHTTP = false
	if _, err := Fetch(context.Background(), refused, consumeAll); !errors.Is(err, ErrInvalidRequest) || !errors.Is(err, ErrPlainHTTP) {
		t.Fatalf("plain HTTP without consent error = %v", err)
	}
	if gets, posts, _ := source.counts(); gets+posts != 0 {
		t.Fatalf("source without consent received %d requests", gets+posts)
	}

	result, err := Fetch(context.Background(), request, consumeAll)
	if err != nil || result.PackBytes != int64(len("PACKpayload")) {
		t.Fatalf("plain HTTP with consent = %+v, %v", result, err)
	}
	if gets, posts, _ := source.counts(); gets != 1 || posts != 1 {
		t.Fatalf("requests = %d/%d, want 1/1", gets, posts)
	}
}

func TestPlainHTTPDefaultsToPort80(t *testing.T) {
	base, err := parseSource("http://source.example/repo.git", 1024, true)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveSource(context.Background(), base, addressPolicy{}, &changingResolver{answers: [][]netip.Addr{{netip.MustParseAddr("8.8.8.8")}}})
	if err != nil || resolved.port != "80" {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
}

func TestExceptionalDestinationsAreSeparateConsents(t *testing.T) {
	for _, test := range []struct {
		address           string
		private, reserved bool
		want              bool
		consent           string
	}{
		{"192.0.2.10", false, false, false, ConsentExceptionalDestination},
		{"192.0.2.10", true, false, false, ConsentExceptionalDestination},
		{"192.0.2.10", false, true, true, ""},
		{"198.18.0.1", false, true, true, ""},
		{"169.254.10.1", false, true, true, ""},
		{"2001:db8::1", false, true, true, ""},
		{"10.0.0.1", false, true, false, ConsentPrivateNetwork},
		{"10.0.0.1", true, false, true, ""},
		// NAT64 reaches the IPv4 address it embeds, which keeps its own rule.
		{"64:ff9b::808:808", false, true, true, ""},
		{"64:ff9b::a00:1", false, true, false, ConsentPrivateNetwork},
		{"64:ff9b::a00:1", true, true, true, ""},
		// Never reachable, whatever the consent.
		{"0.0.0.0", true, true, false, ""},
		{"0.1.2.3", true, true, false, ""},
		{"224.0.0.1", true, true, false, ""},
		{"255.255.255.255", true, true, false, ""},
		{"240.0.0.1", true, true, false, ""},
		{"::", true, true, false, ""},
		{"ff02::1", true, true, false, ""},
		{"fe80::1", true, true, false, ""},
		{"fec0::1", true, true, false, ""},
		{"::c0a8:1", true, true, false, ""},
		{"2001:10::1", true, true, false, ""},
	} {
		address := netip.MustParseAddr(test.address)
		err := addressPolicy{allowPrivate: test.private, allowReserved: test.reserved}.check(address)
		if (err == nil) != test.want {
			t.Errorf("%s private=%v reserved=%v: err = %v, want allowed %v", test.address, test.private, test.reserved, err, test.want)
			continue
		}
		var fetchErr *Error
		if err != nil && (!errors.As(err, &fetchErr) || fetchErr.Consent != test.consent || fetchErr.Address == "" || fetchErr.AddressRange == "") {
			t.Errorf("%s: refusal = %#v, want consent %q with the address and its range", test.address, fetchErr, test.consent)
		}
	}
	scoped := netip.MustParseAddr("fe80::1%en0")
	if err := (addressPolicy{allowPrivate: true, allowReserved: true}).check(scoped); !errors.Is(err, ErrAddressPolicy) {
		t.Fatalf("scoped address error = %v", err)
	}
}

func TestExceptionalDestinationChecksEveryAnswer(t *testing.T) {
	base, _ := url.Parse("https://source.example/repo.git")
	public, reserved, private := netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("10.0.0.1")
	for _, test := range []struct {
		answers []netip.Addr
		policy  addressPolicy
		want    bool
	}{
		{[]netip.Addr{public, reserved}, addressPolicy{}, false},
		{[]netip.Addr{public, reserved}, addressPolicy{allowReserved: true}, true},
		{[]netip.Addr{reserved, private}, addressPolicy{allowReserved: true}, false},
		{[]netip.Addr{reserved, private}, addressPolicy{allowReserved: true, allowPrivate: true}, true},
	} {
		resolver := &changingResolver{answers: [][]netip.Addr{test.answers}}
		resolved, err := resolveSource(context.Background(), base, test.policy, resolver)
		if (err == nil) != test.want {
			t.Errorf("answers %v policy %+v: err = %v", test.answers, test.policy, err)
		}
		if err == nil && resolved.selected != test.answers[0] {
			t.Errorf("selected %v, want the first checked answer", resolved.selected)
		}
	}
}

func TestSameOriginRedirectMovesTheRepositoryURL(t *testing.T) {
	source := packSource(t, "Bearer same.token")
	server, request := startPlainSource(t, source)
	source.handler = func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/old.git/info/refs" {
			return false
		}
		http.Redirect(writer, request, "/group/repo.git/info/refs?service=git-upload-pack", http.StatusMovedPermanently)
		return true
	}
	request.URL = server.URL + "/old.git"
	request.Authentication.BearerToken = "same.token"

	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrRedirect) {
		t.Fatalf("redirect under the default policy = %v", err)
	}
	request.Redirects = RedirectSameOrigin
	if _, err := Fetch(context.Background(), request, consumeAll); err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
	// The pack request went to the redirected repository URL, with the
	// source's credentials, which the scripted source checked.
	if gets, posts, _ := source.counts(); gets != 1 || posts != 1 {
		t.Fatalf("requests at the new URL = %d/%d", gets, posts)
	}
}

func TestRedirectToAnotherOriginNeedsApproval(t *testing.T) {
	target, count := countingServer(t, false)
	source := packSource(t, "Bearer origin.token")
	_, request := startPlainSource(t, source)
	redirectDiscovery(source, http.StatusFound, target.URL+"/group/repo.git/info/refs?service=git-upload-pack")
	request.Authentication.BearerToken = "origin.token"
	request.Redirects = RedirectSameOrigin

	_, err := Fetch(context.Background(), request, consumeAll)
	var fetchErr *Error
	if !errors.As(err, &fetchErr) || !errors.Is(err, ErrRedirect) || fetchErr.RedirectOrigin != target.URL {
		t.Fatalf("cross-origin redirect under same-origin = %#v", err)
	}
	if strings.Contains(err.Error(), "origin.token") {
		t.Fatalf("error disclosed the credential: %v", err)
	}
	if count.Load() != 0 {
		t.Fatalf("unapproved origin received %d requests", count.Load())
	}
}

func TestApprovedOriginGetsNoCredentials(t *testing.T) {
	mirror := packSource(t, "")
	mirrorServer, _ := startPlainSource(t, mirror)
	source := packSource(t, "Bearer source.token")
	_, request := startPlainSource(t, source)
	redirectDiscovery(source, http.StatusTemporaryRedirect, mirrorServer.URL+"/group/repo.git/info/refs?service=git-upload-pack")
	request.Authentication.BearerToken = "source.token"
	request.Redirects = RedirectApproved
	request.ApprovedRedirectOrigin = mirrorServer.URL

	result, err := Fetch(context.Background(), request, consumeAll)
	if err != nil || result.PackBytes == 0 {
		t.Fatalf("approved redirect = %+v, %v", result, err)
	}
	// The mirror expects no Authorization header and reports one as a test
	// error; it served both the discovery and the pack.
	if gets, posts, _ := mirror.counts(); gets != 1 || posts != 1 {
		t.Fatalf("mirror requests = %d/%d", gets, posts)
	}
	if _, posts, _ := source.counts(); posts != 0 {
		t.Fatalf("source received %d pack requests after redirecting", posts)
	}
}

func TestApprovedOriginGetsNoSourceTrust(t *testing.T) {
	// Both test servers use the same certificate. The source's private CA
	// trusts it for the source only, so the approved origin fails TLS.
	target, count := countingServer(t, true)
	source := &scriptedSource{t: t}
	_, request := startSource(t, source)
	redirectDiscovery(source, http.StatusFound, target.URL+"/group/repo.git/info/refs?service=git-upload-pack")
	request.Redirects = RedirectApproved
	request.ApprovedRedirectOrigin = target.URL
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrConnection) {
		t.Fatalf("approved origin with only the source's CA = %v", err)
	}
	if count.Load() != 0 {
		t.Fatalf("approved origin served %d requests without verified TLS", count.Load())
	}
}

func TestRedirectDowngradeNeedsPlainHTTPConsent(t *testing.T) {
	target, count := countingServer(t, false)
	source := &scriptedSource{t: t}
	_, request := startSource(t, source)
	redirectDiscovery(source, http.StatusFound, target.URL+"/group/repo.git/info/refs?service=git-upload-pack")
	request.Redirects = RedirectApproved
	request.ApprovedRedirectOrigin = strings.Replace(target.URL, "http://", "https://", 1)
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrRedirectDowngrade) {
		t.Fatalf("HTTPS to HTTP redirect = %v", err)
	}
	if count.Load() != 0 {
		t.Fatalf("downgraded target received %d requests", count.Load())
	}
}

func TestRedirectLoopsAndChainsAreBounded(t *testing.T) {
	source := packSource(t, "")
	server, request := startPlainSource(t, source)
	request.Redirects = RedirectSameOrigin
	// /hopN redirects to /hop(N-1), and /hop0 to the repository. /loopA and
	// /loopB redirect to each other.
	next := map[string]string{"/loopA": "/loopB", "/loopB": "/loopA", "/hop0": "/group/repo.git"}
	for step := 1; step <= maxRedirects; step++ {
		next["/hop"+strconv.Itoa(step)] = "/hop" + strconv.Itoa(step-1)
	}
	var answered atomic.Int32
	source.handler = func(writer http.ResponseWriter, request *http.Request) bool {
		target, found := next[strings.TrimSuffix(request.URL.Path, "/info/refs")]
		if !found {
			return false
		}
		answered.Add(1)
		http.Redirect(writer, request, target+"/info/refs?service=git-upload-pack", http.StatusFound)
		return true
	}

	request.URL = server.URL + "/loopA"
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrRedirectLoop) {
		t.Fatalf("loop = %v", err)
	}
	request.URL = server.URL + "/hop" + strconv.Itoa(maxRedirects-1)
	if _, err := Fetch(context.Background(), request, consumeAll); err != nil {
		t.Fatalf("%d redirects: %v", maxRedirects, err)
	}
	answered.Store(0)
	request.URL = server.URL + "/hop" + strconv.Itoa(maxRedirects)
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("%d redirects = %v", maxRedirects+1, err)
	}
	if answered.Load() != maxRedirects+1 {
		t.Fatalf("redirects answered = %d, want %d", answered.Load(), maxRedirects+1)
	}
}

func TestRedirectTargetMustStayARepositoryAddress(t *testing.T) {
	source := packSource(t, "")
	_, request := startPlainSource(t, source)
	request.Redirects = RedirectSameOrigin
	for _, location := range []string{"/elsewhere", "/group/repo.git/info/refs?service=git-receive-pack", "/group/repo.git/info/refs?service=git-upload-pack#x", "ftp://example.com/x/info/refs?service=git-upload-pack"} {
		redirectDiscovery(source, http.StatusFound, location)
		if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrRedirectTarget) {
			t.Errorf("redirect to %q = %v", location, err)
		}
	}
}

func TestPackRequestRedirectIsNotReplayed(t *testing.T) {
	target, count := countingServer(t, false)
	source := packSource(t, "")
	_, request := startPlainSource(t, source)
	request.Redirects = RedirectApproved
	request.ApprovedRedirectOrigin = target.URL
	source.handler = func(writer http.ResponseWriter, request *http.Request) bool {
		if request.Method != http.MethodPost {
			return false
		}
		writer.Header().Set("Location", target.URL+"/group/repo.git/git-upload-pack")
		writer.WriteHeader(http.StatusTemporaryRedirect)
		return true
	}
	if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrRedirectRequest) {
		t.Fatalf("307 on the pack request = %v", err)
	}
	if count.Load() != 0 {
		t.Fatalf("307 target received %d requests", count.Load())
	}
}

// A redirect to a new host name is resolved once, every answer is checked
// under the source's address policy, and the connection is pinned to the
// checked address.
func TestRedirectOriginIsResolvedCheckedAndPinned(t *testing.T) {
	mirror := packSource(t, "")
	mirrorServer, _ := startPlainSource(t, mirror)
	port := mirrorServer.Listener.Addr().(*net.TCPAddr).Port
	origin := "http://mirror.test:" + strconv.Itoa(port)
	source := packSource(t, "")
	_, request := startPlainSource(t, source)
	redirectDiscovery(source, http.StatusFound, origin+"/group/repo.git/info/refs?service=git-upload-pack")
	request.Redirects = RedirectApproved
	request.ApprovedRedirectOrigin = origin

	reserved := &changingResolver{answers: [][]netip.Addr{{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("192.0.2.1")}}}
	_, err := fetch(context.Background(), request, consumeAll, reserved)
	var fetchErr *Error
	if !errors.As(err, &fetchErr) || fetchErr.Consent != ConsentExceptionalDestination || fetchErr.Address != "192.0.2.1" {
		t.Fatalf("redirect to a name with a reserved answer = %v", err)
	}
	if mirrorGets, _, _ := mirror.counts(); mirrorGets != 0 {
		t.Fatalf("refused origin received %d requests", mirrorGets)
	}

	pinned := &changingResolver{answers: [][]netip.Addr{{netip.MustParseAddr("127.0.0.1")}}}
	if _, err := fetch(context.Background(), request, consumeAll, pinned); err != nil {
		t.Fatalf("redirect to a checked name: %v", err)
	}
	if pinned.calls != 1 {
		t.Fatalf("redirect origin resolved %d times, want once", pinned.calls)
	}
	if gets, posts, _ := mirror.counts(); gets != 1 || posts != 1 {
		t.Fatalf("pinned mirror requests = %d/%d", gets, posts)
	}
}

func TestRedirectOriginForm(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Mirror.Example":      "https://mirror.example",
		"https://mirror.example:443/": "https://mirror.example",
		"https://mirror.example:8443": "https://mirror.example:8443",
		"https://[2001:db8::1]:443":   "https://[2001:db8::1]",
	} {
		if got, err := ParseRedirectOrigin(raw, false); err != nil || got != want {
			t.Errorf("ParseRedirectOrigin(%q) = %q, %v, want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "mirror.example", "https://mirror.example/path", "https://user@mirror.example", "https://mirror.example?x", "ftp://mirror.example", "http://mirror.example"} {
		if _, err := ParseRedirectOrigin(raw, false); err == nil {
			t.Errorf("ParseRedirectOrigin(%q) accepted", raw)
		}
	}
	if got, err := ParseRedirectOrigin("http://mirror.example:80", true); err != nil || got != "http://mirror.example" {
		t.Errorf("plain HTTP origin with consent = %q, %v", got, err)
	}
}

func TestDependentBoundsFollowTheirLimits(t *testing.T) {
	defaults := DefaultLimits()
	derived, err := EffectiveLimits(Limits{})
	if err != nil || derived.MaxTotalBodyBytes != defaults.MaxTotalBodyBytes || derived.MaxRequestBytes != defaults.MaxRequestBytes {
		t.Fatalf("default limits = %+v, %v", derived, err)
	}
	larger := Limits{MaxPackBytes: 64 << 30}
	larger.Advertisement.MaxTotalBytes = 32 << 20
	larger.Advertisement.MaxRefRecords = 200_000
	derived, err = EffectiveLimits(larger)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(64<<30) + sidebandOverhead(64<<30) + 2*(32<<20) + 64; derived.MaxTotalBodyBytes != want {
		t.Fatalf("total body = %d, want %d", derived.MaxTotalBodyBytes, want)
	}
	if derived.MaxRequestBytes < int64(200_001*wantPacketBytes) {
		t.Fatalf("request bound %d cannot hold 200,000 wants", derived.MaxRequestBytes)
	}
	for _, limits := range []Limits{{MaxPackBytes: math.MaxInt64}, {MaxPackBytes: 1 << 20, Advertisement: importgit.Limits{MaxTotalBytes: math.MaxInt64 / 2}}} {
		if _, err := EffectiveLimits(limits); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("overflowing limits %+v = %v", limits, err)
		}
	}
}
