package prclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"owngit/internal/pullrequest"
)

const (
	maximumRequest  = 64 << 10
	maximumResponse = 4 << 20
)

var errRedirectRefused = errors.New("redirect refused")

type Error struct {
	Code    string
	Message string
	Details json.RawMessage
	Cause   error
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
	password   string
	httpClient *http.Client
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

func New(server *url.URL, password string) *Client {
	return &Client{
		server: server, password: password,
		httpClient: &http.Client{
			Timeout:       35 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirectRefused },
		},
	}
}

func (client *Client) Do(ctx context.Context, method, apiPath string, input any) ([]byte, error) {
	if client == nil || client.server == nil || client.httpClient == nil {
		return nil, &Error{Code: "client_unavailable", Message: "The OwnGit API client is not configured."}
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, &Error{Code: "invalid_request", Message: "The API request could not be encoded.", Cause: err}
		}
		if len(encoded) > maximumRequest {
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
	request.Header.Set("User-Agent", "OwnGit-CLI/1")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if client.password != "" {
		request.SetBasicAuth("owngit", client.password)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		if errors.Is(err, errRedirectRefused) {
			return nil, &Error{Code: "redirect_refused", Message: "The OwnGit API returned a redirect. Credentials were not sent to the redirect target.", Cause: err}
		}
		return nil, &Error{Code: "connection_failed", Message: "The OwnGit server request failed.", Cause: err}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maximumResponse+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, &Error{Code: "invalid_response", Message: "The OwnGit API response could not be read.", Cause: err}
	}
	if len(content) > maximumResponse {
		return nil, &Error{Code: "response_too_large", Message: "The OwnGit API response exceeds the supported size."}
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		return nil, &Error{Code: "invalid_response", Message: "The OwnGit API returned a non-JSON response."}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope pullrequest.ErrorEnvelope
		if err := json.Unmarshal(content, &envelope); err != nil || envelope.OK || envelope.Error.Code == "" || envelope.Error.Message == "" {
			return nil, &Error{Code: "invalid_response", Message: fmt.Sprintf("The OwnGit API returned HTTP %d without a valid error object.", response.StatusCode)}
		}
		return nil, &Error{Code: envelope.Error.Code, Message: envelope.Error.Message, Details: envelope.Error.Details}
	}
	var success struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(content, &success); err != nil || !success.OK {
		return nil, &Error{Code: "invalid_response", Message: "The OwnGit API returned an invalid success object."}
	}
	return content, nil
}
