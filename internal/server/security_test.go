package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
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
	for _, value := range []string{"//example.invalid", `/\\example.invalid`, "https://example.invalid", "/safe\r\nLocation: x",
		"/\t/example.invalid", "/\n/example.invalid", "/\r/example.invalid", "/\x00x", "/\x1b/example.invalid", "/\x7f/example.invalid"} {
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

// Until trusted proxies exist, forwarded headers from any peer change neither
// the Host check, the Origin check, cookie security nor the lockout key.
func TestForwardedHeadersFromDirectPeersChangeNothing(t *testing.T) {
	app := newConfiguredApp(t)
	app.Hosts = NewHostPolicy("owngit.internal")
	handler := app.Handler()
	spoof := func(request *http.Request, client string) {
		request.Header.Set("X-Forwarded-For", client)
		request.Header.Set("X-Forwarded-Proto", "https")
		request.Header.Set("X-Forwarded-Host", "owngit.internal")
		request.Header.Set("Forwarded", "for="+client+";proto=https;host=owngit.internal")
	}

	unknown := httptest.NewRequest(http.MethodGet, "/", nil)
	unknown.Host = "attacker.invalid"
	spoof(unknown, "127.0.0.1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, unknown)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("forwarded approved Host passed the Host check: status=%d", response.Code)
	}

	httpsOrigin := httptest.NewRequest(http.MethodGet, "/settings", nil)
	httpsOrigin.Host = "owngit.internal"
	httpsOrigin.Header.Set("Origin", "https://owngit.internal")
	spoof(httpsOrigin, "127.0.0.1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httpsOrigin)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forwarded scheme satisfied an https Origin: status=%d", response.Code)
	}

	page := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	page.Host = "owngit.internal"
	spoof(page, "127.0.0.1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, page)
	for _, cookie := range response.Result().Cookies() {
		if cookie.Secure {
			t.Fatalf("cookie %s is Secure on a plain HTTP connection with a forwarded scheme", cookie.Name)
		}
	}

	// Wrong administrator passwords from one peer lock that peer out, even
	// when each attempt claims another forwarded client address.
	for attempt := 0; attempt < 4; attempt++ {
		login := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
		login.Host = "owngit.internal"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, login)
		preauth := ""
		for _, cookie := range response.Result().Cookies() {
			if cookie.Name == preauthCookie {
				preauth = cookie.Value
			}
		}
		form := url.Values{"csrf": {preauth}, "admin_password": {"wrong-password"}, "next": {"/"}}
		post := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
		post.Host = "owngit.internal"
		post.RemoteAddr = "192.0.2.50:1000"
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		post.Header.Set("Origin", "http://owngit.internal")
		post.AddCookie(&http.Cookie{Name: preauthCookie, Value: preauth})
		spoof(post, "203.0.113."+strconv.Itoa(attempt+1))
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, post)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password attempt %d status=%d", attempt, response.Code)
		}
	}
	if err := app.Auth.VerifyCredential(context.Background(), "admin", "admin-password", "192.0.2.50"); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatalf("peer 192.0.2.50 is not locked out: %v", err)
	}
	for attempt := 1; attempt <= 4; attempt++ {
		if err := app.Auth.VerifyCredential(context.Background(), "admin", "admin-password", "203.0.113."+strconv.Itoa(attempt)); err != nil {
			t.Fatalf("forwarded address 203.0.113.%d was charged: %v", attempt, err)
		}
	}
}
