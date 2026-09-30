package importfetch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"owngit/internal/importgit"
)

const (
	advertisementMediaType = "application/x-git-upload-pack-advertisement"
	requestMediaType       = "application/x-git-upload-pack-request"
	resultMediaType        = "application/x-git-upload-pack-result"
)

// Fetch obtains one advertised source snapshot and, when nonempty, one raw
// full PACK. It performs no retry. Each origin the fetch reaches is resolved
// once, its whole answer set is checked, and every request to it dials one
// selected literal address while TLS verifies the original hostname.
func Fetch(ctx context.Context, request Request, consume PackConsumer) (*Result, error) {
	return fetch(ctx, request, consume, net.DefaultResolver)
}

func fetch(ctx context.Context, request Request, consume PackConsumer, lookup resolver) (*Result, error) {
	if ctx == nil {
		return nil, fetchError("validate request", ErrInvalidRequest, nil)
	}
	limits, err := EffectiveLimits(request.Limits)
	if err != nil {
		return nil, err
	}
	base, err := parseSource(request.URL, limits.MaxURLBytes, request.AllowPlainHTTP)
	if err != nil {
		return nil, err
	}
	authentication, err := validateAuthentication(request.Authentication, limits.MaxCredentialBytes)
	if err != nil {
		return nil, err
	}
	if len(request.RootCAPEM) > limits.MaxCABundleBytes {
		return nil, fetchError("validate private CA", ErrInvalidRequest, nil)
	}
	bundle := append([]byte(nil), request.RootCAPEM...)
	roots, err := rootPool(bundle)
	if err != nil {
		return nil, err
	}
	connector := &connector{
		policy:         addressPolicy{allowPrivate: request.AllowPrivateNetwork, allowReserved: request.AllowReservedAddresses},
		allowPlainHTTP: request.AllowPlainHTTP,
		redirects:      request.Redirects,
		sourceOrigin:   originKey(base),
		sourceRoots:    roots,
		limits:         limits,
		lookup:         lookup,
	}
	switch request.Redirects {
	case "", RedirectRefuse, RedirectSameOrigin:
	case RedirectApproved:
		approved, err := ParseRedirectOrigin(request.ApprovedRedirectOrigin, request.AllowPlainHTTP)
		if err != nil {
			return nil, fetchError("validate redirect policy", ErrInvalidRequest, err)
		}
		parsed, _ := url.Parse(approved)
		connector.approvedOrigin = originKey(parsed)
	default:
		return nil, fetchError("validate redirect policy", ErrInvalidRequest, nil)
	}

	fetchContext, cancel := context.WithTimeout(ctx, limits.TotalTimeout)
	defer cancel()
	defer connector.close()

	budget := &bodyBudget{remaining: limits.MaxTotalBodyBytes}
	advertisement, client, base, err := fetchAdvertisement(fetchContext, connector, base, authentication, limits, budget)
	if err != nil {
		return nil, err
	}
	if advertisement.Empty {
		return &Result{Advertisement: advertisement, HTTPBodyBytes: budget.consumed}, nil
	}
	if consume == nil {
		return nil, fetchError("validate pack consumer", ErrInvalidRequest, nil)
	}
	build := buildUploadRequest
	if advertisement.ProtocolVersion == 2 {
		build = buildFetchCommand
	}
	requestBody, err := build(advertisement, limits.MaxRequestBytes)
	if err != nil {
		return nil, err
	}
	packBytes, err := fetchPack(fetchContext, client, base, connector.authenticationFor(base, authentication), advertisement, requestBody, consume, limits, budget)
	if err != nil {
		return nil, err
	}
	return &Result{
		Advertisement: advertisement,
		PackBytes:     packBytes,
		HTTPBodyBytes: budget.consumed,
	}, nil
}

// discover requests the advertisement and follows the redirects the
// source's policy allows. It returns the answer, the client pinned to the
// origin that gave it, and the repository URL every later request uses.
func discover(ctx context.Context, connector *connector, base *url.URL, authentication Authentication, limits Limits) (*http.Response, *http.Client, *url.URL, error) {
	client, err := connector.connect(ctx, base)
	if err != nil {
		return nil, nil, nil, err
	}
	visited := map[string]bool{}
	for redirects := 0; ; redirects++ {
		target := endpoint(base, "info/refs", "service=git-upload-pack")
		visited[target.String()] = true
		request, err := newRequest(ctx, http.MethodGet, target, nil, connector.authenticationFor(base, authentication), advertisementMediaType, "", "version=2")
		if err != nil {
			return nil, nil, nil, err
		}
		if len(target.String()) > limits.MaxURLBytes || requestHeaderBytes(request) > limits.MaxHeaderBytes {
			return nil, nil, nil, fetchError("encode advertisement request", ErrInvalidRequest, nil)
		}
		response, err := roundTrip(client, request)
		if err != nil {
			return nil, nil, nil, err
		}
		if !isRedirect(response.StatusCode) {
			return response, client, base, nil
		}
		location := response.Header.Get("Location")
		_ = response.Body.Close()
		next, err := connector.redirectBase(target, location)
		if err != nil {
			return nil, nil, nil, err
		}
		if visited[endpoint(next, "info/refs", "service=git-upload-pack").String()] {
			return nil, nil, nil, fetchError("follow redirect", ErrRedirect, ErrRedirectLoop)
		}
		if redirects+1 > maxRedirects {
			return nil, nil, nil, fetchError("follow redirect", ErrRedirect, ErrTooManyRedirects)
		}
		if client, err = connector.connect(ctx, next); err != nil {
			return nil, nil, nil, err
		}
		base = next
	}
}

// fetchAdvertisement reads what the source advertises. It asks for Git
// protocol v2. A v2 server answers with its capabilities, and an ls-refs
// command then lists only HEAD, branches and tags (importgit.LsRefsPrefixes),
// so refs an import does not use, such as pull request refs, are neither
// listed nor counted. A server without v2 answers with its v0 or v1
// advertisement of every ref, which is used as before.
func fetchAdvertisement(ctx context.Context, connector *connector, base *url.URL, authentication Authentication, limits Limits, budget *bodyBudget) (*importgit.Advertisement, *http.Client, *url.URL, error) {
	response, client, base, err := discover(ctx, connector, base, authentication, limits)
	if err != nil {
		return nil, nil, nil, err
	}
	defer response.Body.Close()
	if err := validateResponse(response, http.StatusOK, advertisementMediaType); err != nil {
		return nil, nil, nil, err
	}
	if !contentLengthWithin(response, limits.Advertisement.MaxTotalBytes) || !contentLengthWithin(response, budget.remaining) {
		return nil, nil, nil, fetchError("read advertisement", ErrResponseTooLarge, nil)
	}
	advertisement, err := importgit.Parse(budget.reader(response.Body), importgit.Options{
		Service:    importgit.DefaultService,
		Limits:     limits.Advertisement,
		ProtocolV2: true,
	})
	if err != nil {
		return nil, nil, nil, advertisementError(err)
	}
	if advertisement.ProtocolVersion == 2 {
		advertisement, err = listRefs(ctx, client, base, connector.authenticationFor(base, authentication), advertisement, limits, budget)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return advertisement, client, base, nil
}

// listRefs runs the ls-refs command of a protocol v2 server that advertised
// capabilities. Its answer has the advertisement's limits and counts toward
// the total body budget.
func listRefs(ctx context.Context, client *http.Client, base *url.URL, authentication Authentication, capabilities *importgit.Advertisement, limits Limits, budget *bodyBudget) (*importgit.Advertisement, error) {
	body, err := buildLsRefsCommand(capabilities, limits.MaxRequestBytes)
	if err != nil {
		return nil, err
	}
	response, err := postCommand(ctx, client, base, authentication, body, "version=2", limits)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if !contentLengthWithin(response, limits.Advertisement.MaxTotalBytes) || !contentLengthWithin(response, budget.remaining) {
		return nil, fetchError("read advertisement", ErrResponseTooLarge, nil)
	}
	advertisement, err := importgit.ParseLsRefs(budget.reader(response.Body), capabilities, importgit.Options{
		Service: importgit.DefaultService,
		Limits:  limits.Advertisement,
	})
	if err != nil {
		return nil, advertisementError(err)
	}
	return advertisement, nil
}

// advertisementError classifies a failure to read an advertisement or an
// ls-refs answer.
func advertisementError(err error) error {
	if errors.Is(err, errTotalBodyExceeded) || errors.Is(err, importgit.ErrLimitExceeded) {
		return fetchError("read advertisement", ErrResponseTooLarge, advertisementCause(err))
	}
	return fetchError("parse advertisement", ErrAdvertisement, advertisementCause(err))
}

// postCommand POSTs an upload-pack request body with the given Git-Protocol
// value and checks the answer's status and media type.
func postCommand(ctx context.Context, client *http.Client, base *url.URL, authentication Authentication, body []byte, protocol string, limits Limits) (*http.Response, error) {
	target := endpoint(base, "git-upload-pack", "")
	request, err := newRequest(ctx, http.MethodPost, target, bytes.NewReader(body), authentication, resultMediaType, requestMediaType, protocol)
	if err != nil {
		return nil, err
	}
	if len(target.String()) > limits.MaxURLBytes || requestHeaderBytes(request) > limits.MaxHeaderBytes {
		return nil, fetchError("encode upload-pack request", ErrInvalidRequest, nil)
	}
	response, err := do(client, request)
	if err != nil {
		return nil, err
	}
	if err := validateResponse(response, http.StatusOK, resultMediaType); err != nil {
		response.Body.Close()
		return nil, err
	}
	return response, nil
}

// fetchPack requests the pack of the advertised objects and hands it to
// consume. A v0 or v1 answer is a NAK and the raw pack. A protocol v2 answer
// is a packfile section whose pack arrives on side-band 1; sideband reads it
// under the same pack and body limits.
func fetchPack(ctx context.Context, client *http.Client, base *url.URL, authentication Authentication, advertisement *importgit.Advertisement, body []byte, consume PackConsumer, limits Limits, budget *bodyBudget) (int64, error) {
	v2 := advertisement.ProtocolVersion == 2
	protocol := "version=1"
	maximumResponse := limits.MaxPackBytes + 8
	if v2 {
		protocol = "version=2"
		maximumResponse = limits.MaxPackBytes + sidebandOverhead(limits.MaxPackBytes)
	}
	response, err := postCommand(ctx, client, base, authentication, body, protocol, limits)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if !contentLengthWithin(response, maximumResponse) || !contentLengthWithin(response, budget.remaining) {
		return 0, fetchError("read upload-pack response", ErrResponseTooLarge, nil)
	}

	entity := budget.reader(response.Body)
	var source io.Reader = entity
	var sideband *sidebandReader
	if v2 {
		if err := readPackfileSection(entity); err != nil {
			return 0, err
		}
		sideband = &sidebandReader{source: entity}
		source = sideband
	} else if err := readNAK(entity); err != nil {
		return 0, err
	}
	pack := &packReader{source: source, remaining: limits.MaxPackBytes}
	var signature [4]byte
	if _, err := io.ReadFull(pack, signature[:]); err != nil {
		if sideband != nil && sideband.failure != nil && !budget.exceeded {
			return 0, sideband.failure
		}
		return 0, uploadProtocolReadError(err)
	}
	if string(signature[:]) != "PACK" {
		return 0, fetchError("validate pack signature", ErrUploadPackProtocol, nil)
	}

	presented := &observedReader{source: io.MultiReader(bytes.NewReader(signature[:]), pack)}
	consumerErr := consume(ctx, advertisement, presented)
	if pack.exceeded || budget.exceeded {
		return 0, fetchError("consume pack", ErrResponseTooLarge, nil)
	}
	// The server's own error, or a broken side-band, explains a pack that
	// ended early better than the consumer that read it.
	if sideband != nil && sideband.failure != nil {
		return 0, sideband.failure
	}
	if consumerErr != nil {
		return 0, fetchError("consume pack", ErrConsumer, safeContextCause(ctx, nil))
	}
	if err := ctx.Err(); err != nil {
		return 0, fetchError("consume pack", ErrConsumer, err)
	}
	if pack.terminalErr != nil && !errors.Is(pack.terminalErr, io.EOF) {
		return 0, fetchError("read pack", ErrConnection, safeContextCause(ctx, nil))
	}
	if !presented.eof {
		var probe [1]byte
		read, err := presented.Read(probe[:])
		switch {
		case pack.exceeded || budget.exceeded || errors.Is(err, errPackExceeded) || errors.Is(err, errTotalBodyExceeded):
			return 0, fetchError("consume pack", ErrResponseTooLarge, nil)
		case sideband != nil && sideband.failure != nil:
			return 0, sideband.failure
		case read > 0:
			return 0, fetchError("consume pack", ErrConsumerStoppedEarly, nil)
		case errors.Is(err, io.EOF):
		case err != nil:
			return 0, fetchError("read pack", ErrConnection, safeContextCause(ctx, nil))
		default:
			return 0, fetchError("consume pack", ErrConsumerStoppedEarly, nil)
		}
	}
	return pack.consumed, nil
}

func newRequest(ctx context.Context, method string, target *url.URL, body io.Reader, authentication Authentication, accept, contentType, protocol string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, fetchError("encode HTTPS request", ErrInvalidRequest, nil)
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Git-Protocol", protocol)
	request.Header.Set("User-Agent", "OwnGit-Importer")
	request.Header.Set("Connection", "close")
	request.Close = true
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	switch {
	case authentication.Basic != nil:
		request.SetBasicAuth(authentication.Basic.Username, authentication.Basic.Password)
	case authentication.BearerToken != "":
		request.Header.Set("Authorization", "Bearer "+authentication.BearerToken)
	}
	return request, nil
}

func validateAuthentication(authentication Authentication, maximum int) (Authentication, error) {
	if authentication.Basic != nil && authentication.BearerToken != "" {
		return Authentication{}, fetchError("validate authentication", ErrInvalidRequest, nil)
	}
	if authentication.Basic != nil {
		if strings.Contains(authentication.Basic.Username, ":") ||
			len(authentication.Basic.Username)+len(authentication.Basic.Password) > maximum {
			return Authentication{}, fetchError("validate authentication", ErrInvalidRequest, nil)
		}
		copy := *authentication.Basic
		authentication.Basic = &copy
	}
	if authentication.BearerToken != "" {
		if len(authentication.BearerToken) > maximum || !validBearerToken(authentication.BearerToken) {
			return Authentication{}, fetchError("validate authentication", ErrInvalidRequest, nil)
		}
	}
	return authentication, nil
}

func validBearerToken(token string) bool {
	padding := false
	for _, character := range token {
		if character == '=' {
			padding = true
			continue
		}
		if padding {
			return false
		}
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-._~+/", character) {
			continue
		}
		return false
	}
	return token != ""
}

// EffectiveLimits fills unset limits with defaults, derives the dependent
// bounds, and refuses values the transport will not run with.
func EffectiveLimits(input Limits) (Limits, error) {
	defaults := DefaultLimits()
	limits := input
	advertisement, err := limits.Advertisement.Effective()
	if err != nil {
		return Limits{}, fetchError("validate advertisement limits", ErrInvalidRequest, nil)
	}
	limits.Advertisement = advertisement
	for _, value := range []int64{limits.MaxRequestBytes, limits.MaxPackBytes, limits.MaxTotalBodyBytes, limits.MaxHeaderBytes} {
		if value < 0 {
			return Limits{}, fetchError("validate limits", ErrInvalidRequest, nil)
		}
	}
	if limits.MaxPackBytes == 0 {
		limits.MaxPackBytes = defaults.MaxPackBytes
	}
	if limits.MaxHeaderBytes == 0 {
		limits.MaxHeaderBytes = defaults.MaxHeaderBytes
	}
	if err := deriveBounds(&limits); err != nil {
		return Limits{}, err
	}
	intLimits := []struct {
		value        *int
		defaultValue int
	}{
		{&limits.MaxURLBytes, defaults.MaxURLBytes},
		{&limits.MaxCredentialBytes, defaults.MaxCredentialBytes},
		{&limits.MaxCABundleBytes, defaults.MaxCABundleBytes},
	}
	for _, item := range intLimits {
		if *item.value < 0 {
			return Limits{}, fetchError("validate limits", ErrInvalidRequest, nil)
		}
		if *item.value == 0 {
			*item.value = item.defaultValue
		}
	}
	durations := []struct {
		value        *time.Duration
		defaultValue time.Duration
	}{
		{&limits.TotalTimeout, defaults.TotalTimeout},
		{&limits.TLSHandshakeTimeout, defaults.TLSHandshakeTimeout},
		{&limits.ResponseHeaderTimeout, defaults.ResponseHeaderTimeout},
	}
	for _, item := range durations {
		if *item.value < 0 {
			return Limits{}, fetchError("validate limits", ErrInvalidRequest, nil)
		}
		if *item.value == 0 {
			*item.value = item.defaultValue
		}
	}
	if limits.MaxPackBytes < 4 || limits.MaxPackBytes > math.MaxInt64/2 ||
		limits.MaxRequestBytes < 1 || limits.MaxTotalBodyBytes < 1 || limits.MaxHeaderBytes < 1 {
		return Limits{}, fetchError("validate limits", ErrInvalidRequest, nil)
	}
	return limits, nil
}

// defaultMaxRequestBytes is the upload-pack request bound for the default
// ref limit, and the least a derived bound is.
const defaultMaxRequestBytes = 8 << 20

// wantPacketBytes bounds one "want <oid>" packet: a length prefix, the
// keyword and a SHA-256 object name.
const wantPacketBytes = 4 + len("want ") + 64 + 1

// deriveBounds fills the bounds that follow from others when a caller left
// them unset: the upload-pack request from the ref limit, which bounds the
// wants, and the total body from the pack and two advertisement answers
// (discovery and ls-refs). Each step is checked before it can overflow.
func deriveBounds(limits *Limits) error {
	if limits.MaxRequestBytes == 0 {
		refs := int64(limits.Advertisement.MaxRefRecords) + 1
		if refs > (math.MaxInt64-(64<<10))/int64(wantPacketBytes) {
			return fetchError("validate limits", ErrInvalidRequest, nil)
		}
		limits.MaxRequestBytes = max(defaultMaxRequestBytes, refs*int64(wantPacketBytes)+64<<10)
	}
	if limits.MaxTotalBodyBytes == 0 {
		pack, advertisement := limits.MaxPackBytes, limits.Advertisement.MaxTotalBytes
		if pack < 0 || pack > math.MaxInt64/2 || advertisement < 0 || advertisement > (math.MaxInt64/2-64)/2 {
			return fetchError("validate limits", ErrInvalidRequest, nil)
		}
		// sidebandOverhead(pack) is below pack/100, so the sum fits.
		limits.MaxTotalBodyBytes = pack + sidebandOverhead(pack) + 2*advertisement + 64
	}
	return nil
}
