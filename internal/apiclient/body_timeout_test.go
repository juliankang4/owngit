package apiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func readTestResponse(t *testing.T, client *Client, kind string) error {
	t.Helper()
	if kind == "json" {
		_, err := client.Do(t.Context(), http.MethodGet, "/api/v1/ping", nil)
		return err
	}
	_, _, err := client.GetBytes(t.Context(), "/api/v1/blob", nil, 1024)
	return err
}

// stallingResponseBody supplies a partial body, then waits for the actual
// request limit. Reading it proves that classification used the body path.
type stallingResponseBody struct {
	ctx  context.Context
	read bool
}

func (body *stallingResponseBody) Read(buffer []byte) (int, error) {
	if !body.read {
		body.read = true
		return copy(buffer, "{"), nil
	}
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (body *stallingResponseBody) Close() error { return nil }

func TestTheRequestLimitStillAppliesWhileReadingTheBody(t *testing.T) {
	for _, kind := range []string{"json", "blob"} {
		t.Run(kind, func(t *testing.T) {
			contentType := "application/json"
			if kind == "blob" {
				contentType = "application/octet-stream"
			}
			var body *stallingResponseBody
			client := New(&url.URL{Scheme: "http", Host: "example.invalid"}, "")
			client.Timeout = 20 * time.Millisecond
			client.httpClient = &http.Client{Transport: timeoutResponseTransport{
				contentType: contentType,
				bodyForRequest: func(request *http.Request) io.ReadCloser {
					body = &stallingResponseBody{ctx: request.Context()}
					return body
				},
			}}
			err := readTestResponse(t, client, kind)
			if body == nil || !body.read {
				t.Fatal("the response body was not read")
			}
			var problem *Error
			if !errors.As(err, &problem) || problem.Code != "connection_failed" || !strings.Contains(problem.Message, "did not answer in time") {
				t.Fatalf("request limit during body read: err=%v", err)
			}
		})
	}
}

func TestACompleteMalformedBodyBeforeTheRequestLimitIsInvalid(t *testing.T) {
	client := New(&url.URL{Scheme: "http", Host: "example.invalid"}, "")
	client.Timeout = time.Hour
	client.httpClient = &http.Client{Transport: timeoutResponseTransport{content: "{", contentType: "application/json"}}
	err := readTestResponse(t, client, "json")
	var problem *Error
	if !errors.As(err, &problem) || problem.Code != "invalid_response" || !strings.Contains(problem.Message, "invalid success object") {
		t.Fatalf("malformed body before request limit: err=%v", err)
	}
}

type responseReadError struct {
	err error
}

func (body responseReadError) Read([]byte) (int, error) { return 0, body.err }
func (body responseReadError) Close() error             { return nil }

func TestAnUnrelatedBodyReadErrorKeepsItsCause(t *testing.T) {
	for _, kind := range []string{"json", "blob"} {
		t.Run(kind, func(t *testing.T) {
			cause := errors.New("response read failed")
			client := New(&url.URL{Scheme: "http", Host: "example.invalid"}, "")
			client.Timeout = time.Hour
			client.httpClient = &http.Client{Transport: timeoutResponseTransport{body: responseReadError{err: cause}}}
			err := readTestResponse(t, client, kind)
			message := "The OwnGit API response could not be read."
			if kind == "blob" {
				message = "The OwnGit blob response could not be read."
			}
			var problem *Error
			if !errors.As(err, &problem) || problem.Code != "invalid_response" || problem.Message != message || !errors.Is(err, cause) {
				t.Fatalf("unrelated body error: err=%v", err)
			}
		})
	}
}
