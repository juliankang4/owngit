package apiclient

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// slowHandshakeServer is an HTTPS server that answers every TLS handshake
// only after delay, as Tailscale Serve does while it gets the certificate
// for a new address, and then answers requests with handler. The client
// trusts its certificate. connState, when given, sees the server's
// connections change state.
func slowHandshakeServer(t *testing.T, delay time.Duration, handler http.HandlerFunc, connState ...func(net.Conn, http.ConnState)) *Client {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	if len(connState) > 0 {
		server.Config.ConnState = connState[0]
	}
	server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		time.Sleep(delay)
		return nil, nil
	}}
	server.StartTLS()
	t.Cleanup(server.Close)
	origin, err := ValidateServer(server.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	client := New(origin, "")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := client.AddCertificateAuthorities(certificate); err != nil {
		t.Fatal(err)
	}
	return client
}

func answerOK(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"ok":true}`))
}

// A request's time limit starts once the connection is ready, so a TLS
// handshake that waits longer than that limit, as the first one to a new
// Tailscale address does, still ends in an answer. The server measures how
// long the connection took to carry the request, so the test shows that it
// was longer than the limit; the limit leaves the request itself a full
// second on a slow machine.
func TestTheRequestLimitStartsWhenTheConnectionIsReady(t *testing.T) {
	const limit = time.Second
	var accepted time.Time
	var waited time.Duration
	client := slowHandshakeServer(t, 3*limit, func(writer http.ResponseWriter, request *http.Request) {
		waited = time.Since(accepted)
		answerOK(writer, request)
	}, func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepted = time.Now()
		}
	})
	client.Timeout = limit
	if _, err := client.Do(context.Background(), http.MethodGet, "/api/v1/ping", nil); err != nil {
		t.Fatalf("a handshake longer than the request limit failed the request: %v", err)
	}
	// The handler ran before the answer was read, and the connection
	// opened before that, so both times are set.
	if waited <= limit {
		t.Fatalf("the request reached the server %s after the connection opened, not longer than the limit %s", waited, limit)
	}
}

// A server that accepts the request but does not answer is still given up
// on after the request limit, with a message that says so.
func TestAServerThatDoesNotAnswerIsGivenUpOn(t *testing.T) {
	stop := make(chan struct{})
	client := slowHandshakeServer(t, 0, func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-stop:
		}
	})
	defer close(stop)
	client.Timeout = 200 * time.Millisecond
	finished := make(chan error, 1)
	go func() {
		_, err := client.Do(context.Background(), http.MethodGet, "/api/v1/ping", nil)
		finished <- err
	}()
	// The bound only keeps a hang from reaching the test binary's timeout.
	select {
	case err := <-finished:
		var problem *Error
		if !errors.As(err, &problem) || problem.Code != "connection_failed" || !strings.Contains(problem.Message, "did not answer in time") {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("the request did not end at its limit")
	}
	// A caller's own cancellation is reported as that, not as a timeout.
	ctx, cancel := context.WithCancel(context.Background())
	client.Timeout = time.Minute
	go func() {
		_, err := client.Do(ctx, http.MethodGet, "/api/v1/ping", nil)
		finished <- err
	}()
	cancel()
	if err := <-finished; err == nil || strings.Contains(err.Error(), "did not answer in time") {
		t.Fatalf("a cancelled request: err=%v", err)
	}
}

// Every client waits for a TLS handshake longer than Go's default of ten
// seconds, and longer than the minute Tailscale waits for a certificate.
func TestTheTLSHandshakeMayWaitForACertificate(t *testing.T) {
	defaultTimeout := http.DefaultTransport.(*http.Transport).TLSHandshakeTimeout
	if TLSHandshakeTimeout <= time.Minute || TLSHandshakeTimeout <= defaultTimeout {
		t.Fatalf("TLSHandshakeTimeout=%s", TLSHandshakeTimeout)
	}
	// Both the default transport of a client and the one a private
	// certificate authority gives it.
	for _, client := range []*Client{New(nil, ""), slowHandshakeServer(t, 0, answerOK)} {
		if transport, ok := client.httpClient.Transport.(*http.Transport); !ok || transport.TLSHandshakeTimeout != TLSHandshakeTimeout {
			t.Fatalf("the client's transport=%#v", client.httpClient.Transport)
		}
	}
	if testing.Short() {
		t.Skip("the handshake must outlast Go's default ten seconds")
	}
	// A real handshake past Go's default limit, through the transport that
	// a private certificate authority gives the client.
	slow := slowHandshakeServer(t, defaultTimeout+time.Second, answerOK)
	if _, err := slow.Do(context.Background(), http.MethodGet, "/api/v1/ping", nil); err != nil {
		t.Fatalf("a handshake of %s failed: %v", defaultTimeout+time.Second, err)
	}
}
