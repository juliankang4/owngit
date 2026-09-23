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
// full PACK. It performs no retry. The source host is resolved once, the whole
// answer set is checked, and both HTTPS requests dial one selected literal
// address while TLS verifies the original hostname.
func Fetch(ctx context.Context, request Request, consume PackConsumer) (*Result, error) {
	if ctx == nil {
		return nil, fetchError("validate request", ErrInvalidRequest, nil)
	}
	limits, err := effectiveLimits(request.Limits)
	if err != nil {
		return nil, err
	}
	base, err := parseSource(request.URL, limits.MaxURLBytes)
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

	fetchContext, cancel := context.WithTimeout(ctx, limits.TotalTimeout)
	defer cancel()
	resolved, err := resolveSource(fetchContext, base, request.AllowPrivateNetwork, net.DefaultResolver)
	if err != nil {
		return nil, err
	}
	client, transport := newHTTPClient(resolved, roots, limits)
	defer transport.CloseIdleConnections()

	budget := &bodyBudget{remaining: limits.MaxTotalBodyBytes}
	advertisement, err := fetchAdvertisement(fetchContext, client, resolved.base, authentication, limits, budget)
	if err != nil {
		return nil, err
	}
	if advertisement.Empty {
		return &Result{Advertisement: advertisement, HTTPBodyBytes: budget.consumed}, nil
	}
	if consume == nil {
		return nil, fetchError("validate pack consumer", ErrInvalidRequest, nil)
	}
	requestBody, err := buildUploadRequest(advertisement, limits.MaxRequestBytes)
	if err != nil {
		return nil, err
	}
	packBytes, err := fetchPack(fetchContext, client, resolved.base, authentication, advertisement, requestBody, consume, limits, budget)
	if err != nil {
		return nil, err
	}
	return &Result{
		Advertisement: advertisement,
		PackBytes:     packBytes,
		HTTPBodyBytes: budget.consumed,
	}, nil
}

func fetchAdvertisement(ctx context.Context, client *http.Client, base *url.URL, authentication Authentication, limits Limits, budget *bodyBudget) (*importgit.Advertisement, error) {
	target := endpoint(base, "info/refs", "service=git-upload-pack")
	request, err := newRequest(ctx, http.MethodGet, target, nil, authentication, advertisementMediaType, "")
	if err != nil {
		return nil, err
	}
	if len(target.String()) > limits.MaxURLBytes || requestHeaderBytes(request) > limits.MaxHeaderBytes {
		return nil, fetchError("encode advertisement request", ErrInvalidRequest, nil)
	}
	response, err := do(client, request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if err := validateResponse(response, http.StatusOK, advertisementMediaType); err != nil {
		return nil, err
	}
	if !contentLengthWithin(response, limits.Advertisement.MaxTotalBytes) || !contentLengthWithin(response, budget.remaining) {
		return nil, fetchError("read advertisement", ErrResponseTooLarge, nil)
	}
	advertisement, err := importgit.Parse(budget.reader(response.Body), importgit.Options{
		Service: importgit.DefaultService,
		Limits:  limits.Advertisement,
	})
	if err != nil {
		if errors.Is(err, errTotalBodyExceeded) || errors.Is(err, importgit.ErrLimitExceeded) {
			return nil, fetchError("read advertisement", ErrResponseTooLarge, advertisementCause(err))
		}
		return nil, fetchError("parse advertisement", ErrAdvertisement, advertisementCause(err))
	}
	return advertisement, nil
}

func fetchPack(ctx context.Context, client *http.Client, base *url.URL, authentication Authentication, advertisement *importgit.Advertisement, body []byte, consume PackConsumer, limits Limits, budget *bodyBudget) (int64, error) {
	target := endpoint(base, "git-upload-pack", "")
	request, err := newRequest(ctx, http.MethodPost, target, bytes.NewReader(body), authentication, resultMediaType, requestMediaType)
	if err != nil {
		return 0, err
	}
	if len(target.String()) > limits.MaxURLBytes || requestHeaderBytes(request) > limits.MaxHeaderBytes {
		return 0, fetchError("encode upload-pack request", ErrInvalidRequest, nil)
	}
	response, err := do(client, request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if err := validateResponse(response, http.StatusOK, resultMediaType); err != nil {
		return 0, err
	}
	maximumResponse := limits.MaxPackBytes + 8
	if !contentLengthWithin(response, maximumResponse) || !contentLengthWithin(response, budget.remaining) {
		return 0, fetchError("read upload-pack response", ErrResponseTooLarge, nil)
	}

	entity := budget.reader(response.Body)
	if err := readNAK(entity); err != nil {
		return 0, err
	}
	pack := &packReader{source: entity, remaining: limits.MaxPackBytes}
	var signature [4]byte
	if _, err := io.ReadFull(pack, signature[:]); err != nil {
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

func newRequest(ctx context.Context, method string, target *url.URL, body io.Reader, authentication Authentication, accept, contentType string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, fetchError("encode HTTPS request", ErrInvalidRequest, nil)
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Git-Protocol", "version=1")
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

func effectiveLimits(input Limits) (Limits, error) {
	defaults := DefaultLimits()
	limits := input
	if err := fillAdvertisementLimits(&limits.Advertisement, defaults.Advertisement); err != nil {
		return Limits{}, err
	}
	integerLimits := []struct {
		value        *int64
		defaultValue int64
	}{
		{&limits.MaxRequestBytes, defaults.MaxRequestBytes},
		{&limits.MaxPackBytes, defaults.MaxPackBytes},
		{&limits.MaxTotalBodyBytes, defaults.MaxTotalBodyBytes},
		{&limits.MaxHeaderBytes, defaults.MaxHeaderBytes},
	}
	for _, item := range integerLimits {
		if *item.value < 0 {
			return Limits{}, fetchError("validate limits", ErrInvalidRequest, nil)
		}
		if *item.value == 0 {
			*item.value = item.defaultValue
		}
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
	if limits.MaxPackBytes < 4 || limits.MaxPackBytes > math.MaxInt64-8 ||
		limits.MaxRequestBytes < 1 || limits.MaxTotalBodyBytes < 1 || limits.MaxHeaderBytes < 1 {
		return Limits{}, fetchError("validate limits", ErrInvalidRequest, nil)
	}
	return limits, nil
}

func fillAdvertisementLimits(limits *importgit.Limits, defaults importgit.Limits) error {
	values := []struct {
		value        *int
		defaultValue int
	}{
		{&limits.MaxPacketBytes, defaults.MaxPacketBytes},
		{&limits.MaxRefRecords, defaults.MaxRefRecords},
		{&limits.MaxNameBytes, defaults.MaxNameBytes},
		{&limits.MaxCapabilities, defaults.MaxCapabilities},
		{&limits.MaxCapabilityBytes, defaults.MaxCapabilityBytes},
	}
	for _, item := range values {
		if *item.value < 0 {
			return fetchError("validate advertisement limits", ErrInvalidRequest, nil)
		}
		if *item.value == 0 {
			*item.value = item.defaultValue
		}
	}
	if limits.MaxPacketBytes > 65520 || limits.MaxTotalBytes < 0 {
		return fetchError("validate advertisement limits", ErrInvalidRequest, nil)
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = defaults.MaxTotalBytes
	}
	return nil
}
