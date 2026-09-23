package importfetch

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/importgit"
)

const (
	testSHA1A   = "1111111111111111111111111111111111111111"
	testSHA1B   = "2222222222222222222222222222222222222222"
	testSHA256A = "1111111111111111111111111111111111111111111111111111111111111111"
	testZero1   = "0000000000000000000000000000000000000000"
)

func testPacket(payload string) string {
	return fmt.Sprintf("%04x%s", len(payload)+4, payload)
}

func testAdvertisement(records ...string) string {
	return testPacket("# service=git-upload-pack\n") + "0000" +
		testPacket("version 1\n") + strings.Join(records, "") + "0000"
}

func testRef(oid, name, capabilities string) string {
	payload := oid + " " + name
	if capabilities != "" {
		payload += "\x00" + capabilities
	}
	return testPacket(payload + "\n")
}

type scriptedSource struct {
	t                 *testing.T
	advertisement     string
	postResponse      string
	wantAuthorization string
	getCount          int
	postCount         int
	postBody          string
	mu                sync.Mutex
	handler           func(http.ResponseWriter, *http.Request) bool
}

func (s *scriptedSource) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if s.handler != nil && s.handler(writer, request) {
		return
	}
	if request.Header.Get("Authorization") != s.wantAuthorization {
		s.t.Errorf("source received an unexpected Authorization header")
	}
	if got := request.Header.Get("Git-Protocol"); got != "version=1" {
		s.t.Errorf("Git-Protocol = %q, want version=1", got)
	}
	if got := request.Header.Get("Accept-Encoding"); got != "identity" {
		s.t.Errorf("Accept-Encoding = %q, want identity", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch request.URL.Path {
	case "/group/repo.git/info/refs":
		s.getCount++
		if request.Method != http.MethodGet || request.URL.RawQuery != "service=git-upload-pack" {
			s.t.Errorf("advertisement request = %s %s", request.Method, request.URL.String())
		}
		writer.Header().Set("Content-Type", advertisementMediaType)
		_, _ = io.WriteString(writer, s.advertisement)
	case "/group/repo.git/git-upload-pack":
		s.postCount++
		if request.Method != http.MethodPost {
			s.t.Errorf("upload request method = %s", request.Method)
		}
		if request.Header.Get("Content-Type") != requestMediaType {
			s.t.Errorf("upload Content-Type = %q", request.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			s.t.Errorf("read upload request: %v", err)
		}
		s.postBody = string(body)
		writer.Header().Set("Content-Type", resultMediaType)
		_, _ = io.WriteString(writer, s.postResponse)
	default:
		s.t.Errorf("unexpected source path %q", request.URL.Path)
		http.NotFound(writer, request)
	}
}

func (s *scriptedSource) counts() (int, int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getCount, s.postCount, s.postBody
}

func startSource(t *testing.T, source *scriptedSource) (*httptest.Server, Request) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(source.serveHTTP))
	t.Cleanup(server.Close)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	return server, Request{
		URL:                 server.URL + "/group/repo.git",
		AllowPrivateNetwork: true,
		RootCAPEM:           certificate,
	}
}

func consumeAll(_ context.Context, _ *importgit.Advertisement, reader io.Reader) error {
	_, err := io.Copy(io.Discard, reader)
	return err
}

func TestFetchSHA1ExactWantsAndRawPack(t *testing.T) {
	source := &scriptedSource{
		t: t,
		advertisement: testAdvertisement(
			testRef(testSHA1A, "HEAD", "ofs-delta symref=HEAD:refs/heads/main"),
			testRef(testSHA1A, "refs/heads/main", ""),
			testRef(testSHA1B, "refs/tags/v1", ""),
			testRef(testSHA1A, "refs/tags/v1^{}", ""),
		),
		postResponse: "0008NAK\nPACKpayload",
	}
	_, request := startSource(t, source)
	var pack string
	result, err := Fetch(context.Background(), request, func(_ context.Context, advertisement *importgit.Advertisement, reader io.Reader) error {
		if advertisement.ObjectFormat != importgit.FormatSHA1 {
			t.Errorf("object format = %q", advertisement.ObjectFormat)
		}
		content, readErr := io.ReadAll(reader)
		pack = string(content)
		return readErr
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if pack != "PACKpayload" || result.PackBytes != int64(len(pack)) {
		t.Fatalf("pack = %q, bytes = %d", pack, result.PackBytes)
	}
	_, posts, body := source.counts()
	want := testPacket("want "+testSHA1A+" ofs-delta\n") +
		testPacket("want "+testSHA1B+"\n") + "0000" + testPacket("done\n")
	if posts != 1 || body != want {
		t.Fatalf("posts = %d, request = %q, want %q", posts, body, want)
	}
	if strings.Contains(body, "^{}") {
		t.Fatal("peeled object was requested separately")
	}
}

func TestFetchSHA256RequestsObjectFormat(t *testing.T) {
	source := &scriptedSource{
		t:             t,
		advertisement: testAdvertisement(testRef(testSHA256A, "refs/heads/main", "object-format=sha256 ofs-delta")),
		postResponse:  "0008NAK\nPACKsha256",
	}
	_, request := startSource(t, source)
	if _, err := Fetch(context.Background(), request, consumeAll); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	_, _, body := source.counts()
	want := testPacket("want "+testSHA256A+" object-format=sha256\n") + "0000" + testPacket("done\n")
	if body != want {
		t.Fatalf("request = %q, want %q", body, want)
	}
}

func TestFetchEmptyAdvertisementDoesNotPostOrInvokeConsumer(t *testing.T) {
	source := &scriptedSource{
		t:             t,
		advertisement: testAdvertisement(testRef(testZero1, "capabilities^{}", "ofs-delta")),
	}
	_, request := startSource(t, source)
	invoked := false
	result, err := Fetch(context.Background(), request, func(context.Context, *importgit.Advertisement, io.Reader) error {
		invoked = true
		return nil
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	gets, posts, _ := source.counts()
	if !result.Advertisement.Empty || invoked || gets != 1 || posts != 0 {
		t.Fatalf("empty=%v invoked=%v requests=%d/%d", result.Advertisement.Empty, invoked, gets, posts)
	}
}

func TestFetchAuthenticationStaysOnSelectedOriginAndOutOfErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		auth Authentication
		want string
	}{
		{name: "basic", auth: Authentication{Basic: &BasicAuth{Username: "reader", Password: "sensitive-password"}}, want: "Basic cmVhZGVyOnNlbnNpdGl2ZS1wYXNzd29yZA=="},
		{name: "bearer", auth: Authentication{BearerToken: "sensitive.token"}, want: "Bearer sensitive.token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &scriptedSource{
				t:                 t,
				advertisement:     testAdvertisement(testRef(testSHA1A, "refs/heads/main", "ofs-delta")),
				postResponse:      "0008NAK\nPACKbody",
				wantAuthorization: test.want,
			}
			_, request := startSource(t, source)
			request.Authentication = test.auth
			_, err := Fetch(context.Background(), request, func(context.Context, *importgit.Advertisement, io.Reader) error {
				return errors.New("consumer sensitive body")
			})
			if !errors.Is(err, ErrConsumer) {
				t.Fatalf("error = %v, want consumer failure", err)
			}
			if strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("error disclosed secret or consumer text: %v", err)
			}
		})
	}
}

func TestFetchPrivateCAIsExplicit(t *testing.T) {
	source := &scriptedSource{
		t:             t,
		advertisement: testAdvertisement(testRef(testZero1, "capabilities^{}", "ofs-delta")),
	}
	_, trusted := startSource(t, source)
	untrusted := trusted
	untrusted.RootCAPEM = nil
	if _, err := Fetch(context.Background(), untrusted, nil); !errors.Is(err, ErrConnection) {
		t.Fatalf("untrusted error = %v, want connection failure", err)
	}
	if _, err := Fetch(context.Background(), trusted, nil); err != nil {
		t.Fatalf("trusted Fetch: %v", err)
	}
}

func TestFetchRefusesRedirectWithoutContactingTarget(t *testing.T) {
	var sinkRequests int
	sink := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		sinkRequests++
	}))
	defer sink.Close()
	source := &scriptedSource{t: t}
	source.handler = func(writer http.ResponseWriter, request *http.Request) bool {
		if strings.HasSuffix(request.URL.Path, "/info/refs") {
			if request.Header.Get("Authorization") != "Bearer redirect-secret" {
				t.Errorf("selected origin did not receive its authorization header")
			}
			http.Redirect(writer, request, sink.URL+"/sink", http.StatusFound)
			return true
		}
		return false
	}
	_, request := startSource(t, source)
	request.Authentication.BearerToken = "redirect-secret"
	if _, err := Fetch(context.Background(), request, nil); !errors.Is(err, ErrRedirect) {
		t.Fatalf("error = %v, want redirect refusal", err)
	}
	if sinkRequests != 0 {
		t.Fatalf("redirect target received %d requests", sinkRequests)
	}
}

func TestFetchRejectsUnsupportedEncodingAndOversizedHeaders(t *testing.T) {
	t.Run("content encoding", func(t *testing.T) {
		source := &scriptedSource{t: t}
		source.handler = func(writer http.ResponseWriter, request *http.Request) bool {
			if !strings.HasSuffix(request.URL.Path, "/info/refs") {
				return false
			}
			writer.Header().Set("Content-Type", advertisementMediaType)
			writer.Header().Set("Content-Encoding", "gzip")
			_, _ = io.WriteString(writer, "remote body must not escape")
			return true
		}
		_, request := startSource(t, source)
		if _, err := Fetch(context.Background(), request, nil); !errors.Is(err, ErrContentEncoding) {
			t.Fatalf("error = %v, want content-encoding refusal", err)
		} else if strings.Contains(err.Error(), "remote body") {
			t.Fatalf("error disclosed response body: %v", err)
		}
	})

	t.Run("response headers", func(t *testing.T) {
		source := &scriptedSource{t: t}
		source.handler = func(writer http.ResponseWriter, request *http.Request) bool {
			if !strings.HasSuffix(request.URL.Path, "/info/refs") {
				return false
			}
			writer.Header().Set("Content-Type", advertisementMediaType)
			writer.Header().Set("X-Oversized", strings.Repeat("x", 2048))
			_, _ = io.WriteString(writer, source.advertisement)
			return true
		}
		_, request := startSource(t, source)
		request.Limits.MaxHeaderBytes = 512
		if _, err := Fetch(context.Background(), request, nil); !errors.Is(err, ErrConnection) {
			t.Fatalf("error = %v, want bounded response-header failure", err)
		}
	})
}

func TestFetchIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	source := &scriptedSource{
		t:             t,
		advertisement: testAdvertisement(testRef(testZero1, "capabilities^{}", "ofs-delta")),
	}
	_, request := startSource(t, source)
	if _, err := Fetch(context.Background(), request, nil); err != nil {
		t.Fatalf("Fetch used proxy environment: %v", err)
	}
}

type changingResolver struct {
	answers [][]netip.Addr
	calls   int
}

func (r *changingResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	answer := r.answers[r.calls]
	r.calls++
	return answer, nil
}

func TestAddressPolicyChecksWholePinnedAnswerSetOnce(t *testing.T) {
	base, err := url.Parse("https://source.example/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		addresses []netip.Addr
		consent   bool
	}{
		{
			name:      "mixed public and private without consent",
			addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")},
		},
		{
			name:      "deprecated compatible member with private consent",
			addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("::c0a8:1")},
			consent:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := &changingResolver{answers: [][]netip.Addr{
				test.addresses,
				{netip.MustParseAddr("9.9.9.9")},
			}}
			if _, err := resolveSource(context.Background(), base, test.consent, resolver); !errors.Is(err, ErrAddressPolicy) {
				t.Fatalf("address-set error = %v", err)
			}
			if resolver.calls != 1 {
				t.Fatalf("resolver calls = %d, want one", resolver.calls)
			}
		})
	}
}

func TestAddressPolicyPrivateConsentAndHardRefusals(t *testing.T) {
	tests := []struct {
		address string
		consent bool
		want    bool
	}{
		{"8.8.8.8", false, true},
		{"10.0.0.1", false, false},
		{"10.0.0.1", true, true},
		{"100.64.0.1", false, false},
		{"100.64.0.1", true, true},
		{"127.0.0.1", false, false},
		{"127.0.0.1", true, true},
		{"::1", false, false},
		{"::1", true, true},
		{"::ffff:127.0.0.1", false, false},
		{"::ffff:127.0.0.1", true, true},
		{"::ffff:192.168.0.1", false, false},
		{"::ffff:192.168.0.1", true, true},
		{"::7f00:1", false, false},
		{"::7f00:1", true, false},
		{"::c0a8:1", false, false},
		{"::c0a8:1", true, false},
		{"169.254.1.1", true, false},
		{"224.0.0.1", true, false},
		{"0.0.0.0", true, false},
		{"2001:db8::1", true, false},
	}
	for _, test := range tests {
		if got := addressAllowed(netip.MustParseAddr(test.address), test.consent); got != test.want {
			t.Errorf("addressAllowed(%s, %v) = %v, want %v", test.address, test.consent, got, test.want)
		}
	}
}

func TestPinnedDialerRejectsAChangedOriginBeforeDial(t *testing.T) {
	dialer := &pinnedDialer{host: "source.example", port: "443", selected: netip.MustParseAddr("127.0.0.1")}
	if _, err := dialer.dialContext(context.Background(), "tcp", "other.example:443"); err == nil {
		t.Fatal("changed origin was accepted")
	}
	if _, err := dialer.dialContext(context.Background(), "tcp", "source.example:444"); err == nil {
		t.Fatal("changed port was accepted")
	}
}

func TestMalformedAdvertisementsAndUnsafeLegacyCapabilityDoNotPost(t *testing.T) {
	shallow := testAdvertisement(
		testRef(testSHA1A, "refs/heads/main", "ofs-delta"),
		testPacket("shallow "+testSHA1A+"\n"),
	)
	tests := []struct {
		name string
		body string
		kind error
	}{
		{name: "malformed", body: "not pkt-line", kind: ErrAdvertisement},
		{name: "v2", body: testPacket("version 2\n") + "0000", kind: ErrAdvertisement},
		{name: "shallow", body: shallow, kind: ErrAdvertisement},
		{name: "no safe capability", body: testAdvertisement(testRef(testSHA1A, "refs/heads/main", "agent=example")), kind: ErrUploadPackProtocol},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &scriptedSource{t: t, advertisement: test.body}
			_, request := startSource(t, source)
			if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, test.kind) {
				t.Fatalf("error = %v, want %v", err, test.kind)
			}
			_, posts, _ := source.counts()
			if posts != 0 {
				t.Fatalf("POST count = %d, want zero", posts)
			}
		})
	}
}

func TestUploadPackFramingFailures(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{name: "ACK", response: testPacket("ACK "+testSHA1A+"\n") + "PACKbody"},
		{name: "ERR", response: testPacket("ERR remote detail must not escape\n")},
		{name: "sideband", response: "0008NAK\n" + testPacket("\x01PACKbody")},
		{name: "truncated", response: "0008NAK\nPA"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &scriptedSource{
				t:             t,
				advertisement: testAdvertisement(testRef(testSHA1A, "refs/heads/main", "ofs-delta")),
				postResponse:  test.response,
			}
			_, request := startSource(t, source)
			if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrUploadPackProtocol) {
				t.Fatalf("error = %v, want upload-pack protocol failure", err)
			} else if strings.Contains(err.Error(), "remote detail") {
				t.Fatalf("error disclosed source response: %v", err)
			}
		})
	}
}

func TestPackLimitAndConsumerCompletion(t *testing.T) {
	newRequest := func(t *testing.T) Request {
		t.Helper()
		source := &scriptedSource{
			t:             t,
			advertisement: testAdvertisement(testRef(testSHA1A, "refs/heads/main", "ofs-delta")),
			postResponse:  "0008NAK\nPACKextra",
		}
		_, request := startSource(t, source)
		return request
	}

	t.Run("over limit", func(t *testing.T) {
		request := newRequest(t)
		request.Limits.MaxPackBytes = 4
		if _, err := Fetch(context.Background(), request, consumeAll); !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("error = %v, want response limit", err)
		}
	})
	t.Run("consumer error", func(t *testing.T) {
		request := newRequest(t)
		if _, err := Fetch(context.Background(), request, func(context.Context, *importgit.Advertisement, io.Reader) error {
			return errors.New("source PACK secret")
		}); !errors.Is(err, ErrConsumer) || strings.Contains(err.Error(), "PACK secret") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("consumer stopped early", func(t *testing.T) {
		request := newRequest(t)
		if _, err := Fetch(context.Background(), request, func(context.Context, *importgit.Advertisement, io.Reader) error {
			return nil
		}); !errors.Is(err, ErrConsumerStoppedEarly) {
			t.Fatalf("error = %v, want early consumer refusal", err)
		}
	})
}

func TestBoundedReadersConsumeOnlyOneOverflowByte(t *testing.T) {
	pack := &packReader{source: strings.NewReader("PACKextra"), remaining: 4}
	content, err := io.ReadAll(pack)
	if string(content) != "PACK" || !errors.Is(err, errPackExceeded) || !pack.exceeded || pack.consumed != 5 {
		t.Fatalf("pack content=%q err=%v exceeded=%v consumed=%d", content, err, pack.exceeded, pack.consumed)
	}
	budget := &bodyBudget{remaining: 4}
	content, err = io.ReadAll(budget.reader(strings.NewReader("dataextra")))
	if string(content) != "data" || !errors.Is(err, errTotalBodyExceeded) || !budget.exceeded || budget.consumed != 5 {
		t.Fatalf("body content=%q err=%v exceeded=%v consumed=%d", content, err, budget.exceeded, budget.consumed)
	}
}

func TestFetchCancellationCoversConsumer(t *testing.T) {
	postStarted := make(chan struct{})
	serverCanceled := make(chan struct{})
	handlerDone := make(chan struct{})
	releaseHandler := make(chan struct{})
	source := &scriptedSource{t: t, advertisement: testAdvertisement(testRef(testSHA1A, "refs/heads/main", "ofs-delta"))}
	source.handler = func(writer http.ResponseWriter, request *http.Request) bool {
		if !strings.HasSuffix(request.URL.Path, "/git-upload-pack") {
			return false
		}
		close(postStarted)
		defer close(handlerDone)
		if _, err := io.Copy(io.Discard, request.Body); err != nil {
			t.Errorf("consume upload request body: %v", err)
			return true
		}
		_ = request.Body.Close()
		writer.Header().Set("Content-Type", resultMediaType)
		_, _ = io.WriteString(writer, "0008NAK\nPACK")
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-request.Context().Done():
			close(serverCanceled)
		case <-releaseHandler:
		}
		return true
	}
	_, request := startSource(t, source)
	t.Cleanup(func() {
		close(releaseHandler)
		select {
		case <-postStarted:
			select {
			case <-handlerDone:
			case <-time.After(time.Second):
				t.Errorf("cancellation fixture handler did not stop during cleanup")
			}
		default:
		}
	})
	request.Limits.TotalTimeout = 100 * time.Millisecond
	_, err := Fetch(context.Background(), request, func(ctx context.Context, _ *importgit.Advertisement, reader io.Reader) error {
		var signature [4]byte
		if _, readErr := io.ReadFull(reader, signature[:]); readErr != nil {
			return readErr
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, ErrConsumer) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want bounded consumer deadline", err)
	}
	select {
	case <-serverCanceled:
	case <-time.After(time.Second):
		t.Error("source request context was not canceled after the fetch deadline")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Error("canceled source handler did not exit")
	}
}

func TestEndpointPreservesRepositoryPathEscaping(t *testing.T) {
	base, err := parseSource("https://source.example/group%2Frepo.git/", 1024)
	if err != nil {
		t.Fatal(err)
	}
	advertisement := endpoint(base, "info/refs", "service=git-upload-pack")
	if got, want := advertisement.EscapedPath(), "/group%2Frepo.git/info/refs"; got != want {
		t.Fatalf("escaped path = %q, want %q", got, want)
	}
	if advertisement.RawQuery != "service=git-upload-pack" || base.RawQuery != "" {
		t.Fatalf("endpoint query changed source URL: endpoint=%q source=%q", advertisement.RawQuery, base.RawQuery)
	}
}

func TestInvalidSourceFormsFailBeforeNetwork(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/repo.git",
		"https://user:secret@example.com/repo.git",
		"https://example.com/repo.git?token=secret",
		"https://example.com/repo.git#fragment",
	} {
		if _, err := Fetch(context.Background(), Request{URL: raw}, nil); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("invalid source error = %v", err)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("error disclosed URL secret: %v", err)
		}
	}
}
