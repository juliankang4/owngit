package tailscale

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"time"
)

// OwnGit reads and changes Tailscale Serve through the daemon's LocalAPI, the
// local HTTP interface the tailscale command itself uses. Its serve-config
// resource answers a read with the configuration and a version (the ETag
// header), and applies a change only while the configuration still has the
// version given in the change's If-Match header; otherwise it answers 412
// and changes nothing. Tailscale 1.50 added both. An older daemon ignores
// If-Match, so OwnGit never writes to a daemon whose read has no version.
//
// Where the LocalAPI is depends on the platform and the Tailscale variant
// (localAPIDialer): tailscaled's Unix socket on Linux and with the open
// source tailscaled on macOS, a loopback TCP port with a password for the
// Tailscale app for macOS, and a named pipe on Windows. Tailscale grants the
// same rights there as to the tailscale command run by the same user.

// Dialer connects to Tailscale's LocalAPI and returns the connection and
// the password the LocalAPI asks for, or "" when it asks for none.
type Dialer func(ctx context.Context) (net.Conn, string, error)

// serveConfigURL is the LocalAPI resource of the Serve configuration. The
// LocalAPI accepts this host name on every transport.
const serveConfigURL = "http://local-tailscaled.sock/localapi/v0/serve-config"

// unixSocket returns a Dialer for tailscaled's Unix socket at path.
func unixSocket(path string) Dialer {
	return func(ctx context.Context) (net.Conn, string, error) {
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "unix", path)
		return conn, "", err
	}
}

// serveConfig sends one request to the serve-config resource: a read when
// body is nil, otherwise a change that applies only while the configuration
// has version ifMatch. It returns the version of a read and its body.
func (command Command) serveConfig(ctx context.Context, timeout time.Duration, ifMatch string, body []byte) (string, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if command.LocalAPI == nil {
		return "", nil, &Error{Kind: KindNotRunning}
	}
	conn, password, err := command.LocalAPI(ctx)
	if err != nil {
		return "", nil, dialFailure(ctx, err)
	}
	defer conn.Close()
	// Closing the connection ends a read or write that waits for Tailscale.
	defer context.AfterFunc(ctx, func() { conn.Close() })()

	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	request, err := http.NewRequestWithContext(ctx, method, serveConfigURL, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	if password != "" {
		request.SetBasicAuth("", password)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	// From here on a failed change may have reached Tailscale, so it is a
	// failure with an unknown outcome, never a refusal.
	if err := request.Write(conn); err != nil {
		return "", nil, exchangeFailure(ctx, err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		return "", nil, exchangeFailure(ctx, err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, maxOutput+1))
	switch {
	case err != nil:
		return "", nil, exchangeFailure(ctx, err)
	case len(content) > maxOutput:
		return "", nil, &Error{Kind: KindUnreadable}
	}
	switch response.StatusCode {
	case http.StatusOK:
		return response.Header.Get("Etag"), content, nil
	case http.StatusPreconditionFailed:
		return "", nil, &Error{Kind: KindServeChanged}
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", nil, &Error{Kind: KindPermission, Detail: shorten(string(content))}
	case http.StatusNotFound:
		// A daemon from before the Serve configuration had this resource.
		return "", nil, &Error{Kind: KindOutdated}
	}
	return "", nil, &Error{Kind: KindFailed, Detail: errorText(content)}
}

// errorText is the message of a LocalAPI error answer: the "error" field of
// a JSON answer, or the text.
func errorText(content []byte) string {
	var answer struct{ Error string }
	if json.Unmarshal(content, &answer) == nil && answer.Error != "" {
		return shorten(answer.Error)
	}
	return shorten(string(content))
}

// dialFailure classifies a failed connection to the LocalAPI. Nothing
// reached Tailscale then.
func dialFailure(ctx context.Context, err error) error {
	var failure *Error
	switch {
	case errors.As(err, &failure):
		return failure
	case ctx.Err() != nil:
		return &Error{Kind: KindTimeout}
	case errors.Is(err, fs.ErrPermission):
		return &Error{Kind: KindPermission, Detail: shorten(err.Error())}
	}
	return &Error{Kind: KindNotRunning}
}

// exchangeFailure classifies a request that failed after it was sent.
func exchangeFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &Error{Kind: KindTimeout}
	}
	return &Error{Kind: KindFailed, Detail: shorten(err.Error())}
}
