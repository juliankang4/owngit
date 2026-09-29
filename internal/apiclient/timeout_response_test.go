package apiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"testing"
	"time"
)

// timeoutResponseTransport chooses which side of the request limit delivers
// the response, without racing a server against a timer.
type timeoutResponseTransport struct {
	late           bool
	content        string
	contentType    string
	body           io.ReadCloser
	bodyForRequest func(*http.Request) io.ReadCloser
}

func (transport timeoutResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	httptrace.ContextClientTrace(request.Context()).GotConn(httptrace.GotConnInfo{})
	if transport.late {
		<-request.Context().Done()
	}
	header := make(http.Header)
	if transport.contentType != "" {
		header.Set("Content-Type", transport.contentType)
	}
	body := transport.body
	if transport.bodyForRequest != nil {
		body = transport.bodyForRequest(request)
	}
	if body == nil {
		body = io.NopCloser(strings.NewReader(transport.content))
	}
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: body, Request: request}, nil
}

func TestAResponseAfterTheRequestLimitIsStillATimeout(t *testing.T) {
	for _, content := range []string{"", `{"ok":true}`} {
		t.Run(content, func(t *testing.T) {
			client := New(&url.URL{Scheme: "http", Host: "example.invalid"}, "")
			client.Timeout = time.Millisecond
			client.httpClient = &http.Client{Transport: timeoutResponseTransport{late: true, content: content, contentType: "application/json"}}
			_, err := client.Do(t.Context(), http.MethodGet, "/api/v1/ping", nil)
			var problem *Error
			if !errors.As(err, &problem) || problem.Code != "connection_failed" || !strings.Contains(problem.Message, "did not answer in time") {
				t.Fatalf("response after request limit: err=%v", err)
			}
		})
	}
}

func TestAnEmptyResponseBeforeTheRequestLimitIsInvalid(t *testing.T) {
	client := New(&url.URL{Scheme: "http", Host: "example.invalid"}, "")
	client.Timeout = time.Hour
	client.httpClient = &http.Client{Transport: timeoutResponseTransport{}}
	_, err := client.Do(context.Background(), http.MethodGet, "/api/v1/ping", nil)
	var problem *Error
	if !errors.As(err, &problem) || problem.Code != "invalid_response" || !strings.Contains(problem.Message, "non-JSON") {
		t.Fatalf("empty response before request limit: err=%v", err)
	}
}
