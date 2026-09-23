package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStalledOrdinaryFormTimesOutAndShutdownCompletes(t *testing.T) {
	app, _, _ := newTestApp(t)
	app.HTTPTimeout = 75 * time.Millisecond
	server := serve(t, app.Handler())
	address := strings.TrimPrefix(server.URL, "http://")
	connection, err := net.Dial("tcp", address)
	noErr(t, err)
	defer connection.Close()
	request := "POST /setup/redeem HTTP/1.1\r\nHost: " + address + "\r\nOrigin: " + server.URL + "\r\nContent-Type: application/x-www-form-urlencoded\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n4\r\ncsrf\r\n"
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatal(err)
	}
	noErr(t, connection.SetReadDeadline(time.Now().Add(2*time.Second)))
	readDone := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(connection)
		readDone <- readErr
	}()
	// Shutdown begins while the request body is still incomplete. The app's
	// ordinary-request deadline must release the handler before shutdown's own
	// deadline expires.
	time.Sleep(10 * time.Millisecond)
	shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	noErrf(t, server.Config.Shutdown(shutdownContext), "shutdown while ordinary form was stalled")
	readErr := <-readDone
	if timeout, ok := readErr.(net.Error); ok && timeout.Timeout() {
		t.Fatal("ordinary form connection did not close after its deadline")
	}
}

func TestUnknownHostIsRejectedBeforeApplication(t *testing.T) {
	called := false
	handler := NewHostPolicy("owngit.internal").Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	request := httptest.NewRequest(http.MethodGet, "http://attacker.invalid/", nil)
	request.Host = "attacker.invalid"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest || called {
		t.Fatalf("status=%d called=%v, want 421 before application", response.Code, called)
	}
}

func TestLocalNextRejectsExternalAndBackslashRedirects(t *testing.T) {
	for _, value := range []string{"//example.invalid", `/\\example.invalid`, "https://example.invalid", "/safe\r\nLocation: x"} {
		if got := localNext(value, "/fallback"); got != "/fallback" {
			t.Errorf("localNext(%q)=%q, want fallback", value, got)
		}
	}
	if got := localNext("/repositories/project?ref=main", "/fallback"); got != "/repositories/project?ref=main" {
		t.Fatalf("safe local path changed to %q", got)
	}
}

func TestOriginMustExactlyMatchRequest(t *testing.T) {
	called := false
	handler := NewHostPolicy("owngit.internal").Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	for _, origin := range []string{"http://owngit.internal.evil", "https://owngit.internal", "null", "http://owngit.internal/path"} {
		called = false
		request := httptest.NewRequest(http.MethodPost, "http://owngit.internal/settings", nil)
		request.Host = "owngit.internal"
		request.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || called {
			t.Errorf("origin %q: status=%d called=%v", origin, response.Code, called)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://owngit.internal/settings", nil)
	request.Host = "owngit.internal"
	request.Header.Set("Origin", "http://owngit.internal")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !called {
		t.Fatalf("matching origin status=%d called=%v", response.Code, called)
	}
}
