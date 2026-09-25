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
	// DefaultTimeout bounds one ordinary API request, including the response.
	DefaultTimeout = 35 * time.Second
)

var errRedirectRefused = errors.New("redirect refused")

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
			Timeout:       DefaultTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirectRefused },
		},
	}
}

// http returns the HTTP client with this client's request timeout.
func (client *Client) http() *http.Client {
	if client.Timeout <= 0 {
		return client.httpClient
	}
	configured := *client.httpClient
	configured.Timeout = client.Timeout
	return &configured
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
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &Error{Code: "http_transport_unavailable", Message: "The default HTTP transport is unavailable."}
	}
	configured := transport.Clone()
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
	response, err := client.http().Do(request)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		if errors.Is(err, errRedirectRefused) {
			return nil, &Error{Code: "redirect_refused", Message: "The OwnGit API returned a redirect. Credentials were not sent to the redirect target.", Cause: err}
		}
		return nil, connectionFailed(err)
	}
	defer response.Body.Close()
	responseLimit := client.MaximumResponse
	if responseLimit <= 0 {
		responseLimit = maximumResponse
	}
	limited := io.LimitReader(response.Body, responseLimit+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, responseError(response, &Error{Code: "invalid_response", Message: "The OwnGit API response could not be read.", Cause: err})
	}
	if int64(len(content)) > responseLimit {
		return nil, responseError(response, &Error{Code: "response_too_large", Message: "The OwnGit API response exceeds the supported size."})
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		return nil, responseError(response, &Error{Code: "invalid_response", Message: "The OwnGit API returned a non-JSON response."})
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope pullrequest.ErrorEnvelope
		if err := json.Unmarshal(content, &envelope); err != nil || envelope.OK || envelope.Error.Code == "" || envelope.Error.Message == "" {
			return nil, responseError(response, &Error{Code: "invalid_response", Message: fmt.Sprintf("The OwnGit API returned HTTP %d without a valid error object.", response.StatusCode)})
		}
		return nil, responseError(response, &Error{Code: envelope.Error.Code, Message: envelope.Error.Message, Details: envelope.Error.Details, Status: response.StatusCode})
	}
	var success struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(content, &success); err != nil || !success.OK {
		return nil, &Error{Code: "invalid_response", Message: "The OwnGit API returned an invalid success object."}
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
	response, err := client.http().Do(request)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, nil, connectionFailed(err)
	}
	defer response.Body.Close()
	content, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if readErr != nil {
		return nil, nil, responseError(response, &Error{Code: "invalid_response", Message: "The OwnGit blob response could not be read.", Cause: readErr})
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
		return nil, nil, &Error{Code: "invalid_response", Message: "The OwnGit API returned an invalid source blob type."}
	}
	if int64(len(content)) > limit {
		return nil, nil, &Error{Code: "response_too_large", Message: "The OwnGit source blob exceeds its declared bound."}
	}
	return content, response.Header.Clone(), nil
}
