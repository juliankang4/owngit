package apiclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/pullrequest"
)

func TestValidateServerRequiresOriginAndExplicitHTTPConsent(t *testing.T) {
	for _, raw := range []string{
		"https://user:secret@example.test",
		"https://example.test/path",
		"https://example.test?query=1",
		"file:///tmp/socket",
	} {
		if _, err := ValidateServer(raw, true); errorCode(err) != "invalid_server" {
			t.Errorf("ValidateServer(%q) error=%v code=%q", raw, err, errorCode(err))
		}
	}
	if _, err := ValidateServer("http://example.test", false); errorCode(err) != "insecure_http_confirmation_required" {
		t.Fatalf("HTTP without consent error=%v code=%q", err, errorCode(err))
	}
	if parsed, err := ValidateServer("http://example.test:7654", true); err != nil || parsed.String() != "http://example.test:7654" {
		t.Fatalf("consented private HTTP origin=%v err=%v", parsed, err)
	}
}

func TestClientSendsOnePreauthenticatedRequestAndDoesNotRetry401(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		_, password, ok := request.BasicAuth()
		if !ok || password != "wrong-password" {
			t.Errorf("request did not carry the configured credential")
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(writer).Encode(pullrequest.ErrorEnvelope{
			OK: false, Error: pullrequest.ErrorDescription{Code: "invalid_credentials", Message: "invalid"},
		})
	}))
	defer server.Close()
	origin, err := ValidateServer(server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	client := New(origin, "wrong-password")
	_, err = client.Do(context.Background(), http.MethodGet, "/api/v1/repositories/project/pull-requests", nil)
	if errorCode(err) != "invalid_credentials" {
		t.Fatalf("401 error=%v code=%q", err, errorCode(err))
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("authentication request count=%d, want 1", got)
	}
}

func TestClientRefusesRedirectWithoutContactingTarget(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetRequests.Add(1)
		if request.Header.Get("Authorization") != "" {
			t.Error("redirect target received an Authorization header")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()
	originServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer originServer.Close()
	origin, err := ValidateServer(originServer.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	client := New(origin, "synthetic-password")
	_, err = client.Do(context.Background(), http.MethodPost, "/api/v1/repositories/project/pull-requests", map[string]string{"title": "test"})
	if errorCode(err) != "redirect_refused" {
		t.Fatalf("redirect error=%v code=%q", err, errorCode(err))
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target was contacted %d times", targetRequests.Load())
	}
}

func errorCode(err error) string {
	var problem *Error
	if errors.As(err, &problem) {
		return problem.Code
	}
	return ""
}

// A request that gets no response names the transport cause, never the
// credential or the request URL.
func TestConnectionFailureNamesTheCause(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	address := server.URL
	server.Close()
	origin, err := ValidateServer(address, true)
	if err != nil {
		t.Fatal(err)
	}
	client := New(origin, "synthetic-secret-password")
	_, err = client.Do(context.Background(), http.MethodGet, "/api/v1/repositories/project/pull-requests", nil)
	var problem *Error
	if !errors.As(err, &problem) || problem.Code != "connection_failed" || problem.Status != 0 {
		t.Fatalf("closed server error=%v", err)
	}
	if !strings.HasPrefix(problem.Message, "The OwnGit server request failed: ") || len(problem.Message) <= len("The OwnGit server request failed: ") {
		t.Fatalf("message does not name the cause: %q", problem.Message)
	}
	if strings.Contains(problem.Message, "synthetic-secret-password") || strings.Contains(problem.Message, "/api/v1/") {
		t.Fatalf("message carries the credential or the request path: %q", problem.Message)
	}

	// An error response records its HTTP status.
	refusing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(writer).Encode(pullrequest.ErrorEnvelope{
			OK: false, Error: pullrequest.ErrorDescription{Code: "conflict", Message: "refused"},
		})
	}))
	defer refusing.Close()
	origin, err = ValidateServer(refusing.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(origin, "password").Do(context.Background(), http.MethodGet, "/api/v1/x", nil)
	if !errors.As(err, &problem) || problem.Status != http.StatusConflict {
		t.Fatalf("refusal error=%v", err)
	}
}

// A caller that retries needs the status of any response, even a proxy page
// without an OwnGit error object, and the delay a 503 asked for.
func TestErrorsCarryTheResponseStatusAndRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/proxy" {
			writer.Header().Set("Content-Type", "text/html")
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = writer.Write([]byte("<html>bad gateway</html>"))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Retry-After", "7")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"ok":false,"error":{"code":"state_unavailable","message":"starting"}}`))
	}))
	defer server.Close()
	origin, err := ValidateServer(server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	client := New(origin, "")
	_, err = client.Do(context.Background(), http.MethodGet, "/busy", nil)
	var problem *Error
	if !errors.As(err, &problem) || problem.Status != http.StatusServiceUnavailable || problem.ResponseStatus != http.StatusServiceUnavailable || problem.RetryAfter != 7*time.Second {
		t.Fatalf("503 err=%+v", problem)
	}
	_, err = client.Do(context.Background(), http.MethodGet, "/proxy", nil)
	if !errors.As(err, &problem) || problem.Code != "invalid_response" || problem.Status != 0 || problem.ResponseStatus != http.StatusBadGateway || problem.RetryAfter != 0 {
		t.Fatalf("proxy err=%+v", problem)
	}
}
