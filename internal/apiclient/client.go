package apiclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"time"

	"owngit/internal/pullrequest"
	"owngit/internal/version"
)

const (
	maximumRequest  = 64 << 10
	maximumResponse = 4 << 20
	// DefaultTimeout bounds one ordinary API request, including the
	// response, from when the connection is ready.
	DefaultTimeout = 35 * time.Second
	// TLSHandshakeTimeout bounds the TLS handshake of a new connection.
	// Tailscale Serve answers the first handshake to an address only once
	// it has the certificate, and waits up to one minute for it
	// (ipn/ipnlocal getTLSServeCertForPort), which took 36 seconds in a
	// real tailnet. This bound lets Tailscale's own limit decide.
	TLSHandshakeTimeout = 75 * time.Second
	// dialTimeout bounds opening the TCP connection, as Go's default
	// transport does.
	dialTimeout = 30 * time.Second
)

// errRequestTimeout ends a request that took longer than its time limit.
var errRequestTimeout = errors.New("the server did not answer in time")

// redirectRefused reports a redirect that says nothing more, such as one
// from a proxy. An OwnGit redirect carries an error object, such as
// repository_moved, which is reported as it is.
func redirectRefused() *Error {
	return &Error{Code: "redirect_refused", Message: "The OwnGit API returned a redirect. Credentials were not sent to the redirect target."}
}

// connectionFailed reports a request that got no usable response. The message
// names the transport cause, such as a refused connection or a TLS failure.
// The request URL is left out: the cause alone explains the failure, and the
// URL is already known to the caller.
func connectionFailed(err error) *Error {
	cause := err
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.Err != nil {
		cause = urlError.Err
	}
	return &Error{Code: "connection_failed", Message: "The OwnGit server request failed: " + cause.Error(), Cause: err}
}

type Error struct {
	Code    string
	Message string
	Details json.RawMessage
	Cause   error
	// Status is the HTTP status of an error response from the server, or 0
	// when no valid error response was received.
	Status int
	// ResponseStatus is the HTTP status of any response that was received,
	// including one without a valid error object, such as a proxy's 502 page.
	// It is 0 when no response arrived.
	ResponseStatus int
	// RetryAfter is the delay a 429 or 503 response asked for, or 0.
	RetryAfter time.Duration
}

// retryAfter reads the Retry-After header of a 429 or 503 response, given in
// seconds or as an HTTP date.
func retryAfter(response *http.Response) time.Duration {
	if response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusServiceUnavailable {
		return 0
	}
	value := strings.TrimSpace(response.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(min(seconds, int64(24*time.Hour/time.Second))) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		return max(time.Until(when), 0)
	}
	return 0
}

// responseError fills the response-level fields of a failure built from a
// received response.
func responseError(response *http.Response, problem *Error) *Error {
	problem.ResponseStatus = response.StatusCode
	problem.RetryAfter = retryAfter(response)
	return problem
}

func (problem *Error) Error() string {
	if problem == nil {
		return ""
	}
	return problem.Message
}

func (problem *Error) Unwrap() error {
	if problem == nil {
		return nil
	}
	return problem.Cause
}

func (problem *Error) ErrorCode() string {
	if problem == nil {
		return "internal_error"
	}
	return problem.Code
}

func (problem *Error) ErrorDetails() json.RawMessage {
	if problem == nil {
		return nil
	}
	return problem.Details
}

type Client struct {
	server     *url.URL
	authorize  func(*http.Request)
	httpClient *http.Client
	// MaximumRequest and MaximumResponse override JSON transport bounds for
	// explicitly bounded protocols such as check evidence and source manifests.
	MaximumRequest  int64
	MaximumResponse int64
	// Timeout replaces DefaultTimeout for an explicitly long request, such as
	// a synchronous import run that the server bounds by its own deadline.
	Timeout time.Duration
}

// newTransport is Go's default transport with a TLS handshake that may wait
// for Tailscale to get a certificate (TLSHandshakeTimeout).
func newTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = TLSHandshakeTimeout
	return transport
}

// sharedTransport serves every client without a private certificate
// authority, so clients made one after another, as the MCP server makes one
// per tool call, reuse open connections.
var sharedTransport = newTransport()

// New returns a client that authenticates with the shared general-access
// password, or with no credentials when the password is empty.
func New(server *url.URL, password string) *Client {
	client := newClient(server)
	if password != "" {
		client.authorize = func(request *http.Request) { request.SetBasicAuth("owngit", password) }
	}
	return client
}

// NewBearer returns a client that authenticates with a repository-scoped
// helper or runner credential. The token travels in the Authorization header,
// never in the URL or a command argument.
func NewBearer(server *url.URL, token string) *Client {
	client := newClient(server)
	if token != "" {
		client.authorize = func(request *http.Request) { request.Header.Set("Authorization", "Bearer "+token) }
	}
	return client
}

// NewAdmin returns a client that authenticates with the administrator
// password for helper-credential management.
func NewAdmin(server *url.URL, password string) *Client {
	client := newClient(server)
	if password != "" {
		client.authorize = func(request *http.Request) { request.SetBasicAuth("admin", password) }
	}
	return client
}

func newClient(server *url.URL) *Client {
	return &Client{
		server: server,
		httpClient: &http.Client{
			Transport: sharedTransport,
			// A redirect is never followed, so credentials never reach its
			// target. Do reads the redirect's own answer instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// send sends request and returns the response with the function that ends
// its time limit and reports a timeout, to call once the body is read. The
// limit, Timeout or DefaultTimeout, starts when the connection is ready, so
// a TLS handshake that waits for a certificate does not use it up; opening
// the connection has the transport's own limits, and this one only guards
// against a hang before them.
func (client *Client) send(request *http.Request) (*http.Response, func() error, error) {
	limit := client.Timeout
	if limit <= 0 {
		limit = DefaultTimeout
	}
	ctx, cancel := context.WithCancelCause(request.Context())
	timer := time.AfterFunc(dialTimeout+TLSHandshakeTimeout+limit, func() { cancel(errRequestTimeout) })
	trace := &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { timer.Reset(limit) }}
	done := func() error {
		timer.Stop()
		err := requestTimeoutError(ctx, request)
		cancel(nil)
		return err
	}
	response, err := client.httpClient.Do(request.WithContext(httptrace.WithClientTrace(ctx, trace)))
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		if timeoutErr := done(); timeoutErr != nil {
			err = timeoutErr
		}
		return nil, nil, err
	}
	return response, done, nil
}

func requestTimeoutError(ctx context.Context, request *http.Request) error {
	if errors.Is(context.Cause(ctx), errRequestTimeout) {
		return &url.Error{Op: request.Method, URL: request.URL.String(), Err: errRequestTimeout}
	}
	return nil
}

// readResponseBody ends the request limit after the bounded read. Its own
// timeout takes precedence over any response or body error received later.
func readResponseBody(response *http.Response, done func() error, limit int64, failureMessage string) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if timeoutErr := done(); timeoutErr != nil {
		return nil, connectionFailed(timeoutErr)
	}
	if err != nil {
		return nil, responseError(response, &Error{Code: "invalid_response", Message: failureMessage, Cause: err})
	}
	return content, nil
}

// AddCertificateAuthorities extends the system trust store for a private CA
// without disabling hostname or certificate verification.
func (client *Client) AddCertificateAuthorities(pem []byte) error {
	if client == nil || client.httpClient == nil || len(pem) == 0 {
		return &Error{Code: "invalid_certificate_authority", Message: "The certificate authority file is empty."}
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		return &Error{Code: "invalid_certificate_authority", Message: "The certificate authority file contains no valid certificates."}
	}
	configured := newTransport()
	configured.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	client.httpClient.Transport = configured
	return nil
}

func ValidateServer(raw string, acceptInsecureHTTP bool) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, &Error{Code: "invalid_server", Message: "--server must be an HTTP(S) origin without a path, query, credentials, or fragment."}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, &Error{Code: "invalid_server", Message: "--server must use HTTP or HTTPS."}
	}
	if strings.ContainsAny(parsed.Host, "\x00\r\n ") {
		return nil, &Error{Code: "invalid_server", Message: "--server contains an invalid host."}
	}
	if parsed.Scheme == "http" && !acceptInsecureHTTP {
		return nil, &Error{
			Code:    "insecure_http_confirmation_required",
			Message: "HTTP does not encrypt passwords or pull request metadata. Repeat with --accept-insecure-http only after accepting that risk for this private connection.",
		}
	}
	return parsed, nil
}

func (client *Client) Do(ctx context.Context, method, apiPath string, input any) ([]byte, error) {
	return client.DoWithHeaders(ctx, method, apiPath, input, nil)
}

// DoWithHeaders performs a JSON API request with additional protocol headers.
// Authorization remains client-owned and cannot be overridden by callers.
func (client *Client) DoWithHeaders(ctx context.Context, method, apiPath string, input any, headers map[string]string) ([]byte, error) {
	if client == nil || client.server == nil || client.httpClient == nil {
		return nil, &Error{Code: "client_unavailable", Message: "The OwnGit API client is not configured."}
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, &Error{Code: "invalid_request", Message: "The API request could not be encoded.", Cause: err}
		}
		limit := client.MaximumRequest
		if limit <= 0 {
			limit = maximumRequest
		}
		if int64(len(encoded)) > limit {
			return nil, &Error{Code: "request_too_large", Message: "The API request exceeds the supported size."}
		}
		body = bytes.NewReader(encoded)
	}
	target := client.server.String() + apiPath
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, &Error{Code: "invalid_request", Message: "The API request could not be created.", Cause: err}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "OwnGit-CLI/"+version.Version)
	for name, value := range headers {
		if !strings.EqualFold(name, "Authorization") {
			request.Header.Set(name, value)
		}
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if client.authorize != nil {
		client.authorize(request)
	}
	response, done, err := client.send(request)
	if err != nil {
		return nil, connectionFailed(err)
	}
	defer done()
	defer response.Body.Close()
	responseLimit := client.MaximumResponse
	if responseLimit <= 0 {
		responseLimit = maximumResponse
	}
	content, err := readResponseBody(response, done, responseLimit, "The OwnGit API response could not be read.")
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > responseLimit {
		return nil, responseError(response, &Error{Code: "response_too_large", Message: "The OwnGit API response exceeds the supported size."})
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	redirect := response.StatusCode >= 300 && response.StatusCode < 400
	if mediaErr != nil || mediaType != "application/json" {
		if redirect {
			return nil, responseError(response, redirectRefused())
		}
		return nil, responseError(response, &Error{Code: "invalid_response", Message: "The OwnGit API returned a non-JSON response."})
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope pullrequest.ErrorEnvelope
		if err := json.Unmarshal(content, &envelope); err != nil || envelope.OK || envelope.Error.Code == "" || envelope.Error.Message == "" {
			if redirect {
				return nil, responseError(response, redirectRefused())
			}
			return nil, responseError(response, &Error{Code: "invalid_response", Message: fmt.Sprintf("The OwnGit API returned HTTP %d without a valid error object.", response.StatusCode)})
		}
		return nil, responseError(response, &Error{Code: envelope.Error.Code, Message: envelope.Error.Message, Details: envelope.Error.Details, Status: response.StatusCode})
	}
	var success struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(content, &success); err != nil || !success.OK {
		return nil, responseError(response, &Error{Code: "invalid_response", Message: "The OwnGit API returned an invalid success object."})
	}
	return content, nil
}

// GetBytes reads one bounded application/octet-stream response. It is used for
// exact configured-check source blobs, never for arbitrary URLs.
func (client *Client) GetBytes(ctx context.Context, apiPath string, headers map[string]string, limit int64) ([]byte, http.Header, error) {
	if client == nil || client.server == nil || client.httpClient == nil || limit < 0 {
		return nil, nil, &Error{Code: "client_unavailable", Message: "The OwnGit API client is not configured."}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.server.String()+apiPath, nil)
	if err != nil {
		return nil, nil, &Error{Code: "invalid_request", Message: "The OwnGit API request could not be created.", Cause: err}
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "OwnGit-CLI/"+version.Version)
	for name, value := range headers {
		if !strings.EqualFold(name, "Authorization") {
			request.Header.Set(name, value)
		}
	}
	if client.authorize != nil {
		client.authorize(request)
	}
	response, done, err := client.send(request)
	if err != nil {
		return nil, nil, connectionFailed(err)
	}
	defer done()
	defer response.Body.Close()
	content, readErr := readResponseBody(response, done, limit, "The OwnGit blob response could not be read.")
	if readErr != nil {
		return nil, nil, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope pullrequest.ErrorEnvelope
		if err := json.Unmarshal(content, &envelope); err == nil && !envelope.OK && envelope.Error.Code != "" {
			return nil, nil, responseError(response, &Error{Code: envelope.Error.Code, Message: envelope.Error.Message, Details: envelope.Error.Details, Status: response.StatusCode})
		}
		return nil, nil, responseError(response, &Error{Code: "invalid_response", Message: fmt.Sprintf("The OwnGit API returned HTTP %d for a source blob.", response.StatusCode)})
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/octet-stream" {
		return nil, nil, responseError(response, &Error{Code: "invalid_response", Message: "The OwnGit API returned an invalid source blob type."})
	}
	if int64(len(content)) > limit {
		return nil, nil, responseError(response, &Error{Code: "response_too_large", Message: "The OwnGit source blob exceeds its declared bound."})
	}
	return content, response.Header.Clone(), nil
}
