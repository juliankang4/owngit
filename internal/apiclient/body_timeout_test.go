package apiclient

import (
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestTheRequestLimitStillAppliesWhileReadingTheBody(t *testing.T) {
	for _, kind := range []string{"json", "blob"} {
		t.Run(kind, func(t *testing.T) {
			stop := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				contentType := "application/json"
				if kind == "blob" {
					contentType = "application/octet-stream"
				}
				writer.Header().Set("Content-Type", contentType)
				_, _ = writer.Write([]byte("{"))
				writer.(http.Flusher).Flush()
				select {
				case <-request.Context().Done():
				case <-stop:
				}
			}))
			defer server.Close()
			defer close(stop)
			origin, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			client := New(origin, "")
			client.Timeout = 20 * time.Millisecond
			err = readTestResponse(t, client, kind)
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
