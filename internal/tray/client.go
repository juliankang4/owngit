// Package tray is the OwnGit icon of this computer: what it reads from the
// server and what it shows. The Windows notification area icon is built
// here; other platforms have their own programs that read the same status.
package tray

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"owngit/internal/server"
	"owngit/internal/state"
)

// Condition is what the icon says about the server.
type Condition int

const (
	// Unavailable means the icon could not tell: OwnGit may run but did not
	// answer the status, or its answer could not be read. It never means
	// that OwnGit stopped.
	Unavailable Condition = iota
	// Running means the server answered and asks for nothing.
	Running
	// Attention means the server answered and the owner has something to
	// do: finish setup, update, or repair what the checkup found.
	Attention
	// Stopped means the checkup found that the server does not run.
	Stopped
)

// Report is one reading of the server.
type Report struct {
	Condition Condition
	// Status is the server's answer, for Running and Attention.
	Status *server.TrayStatus
	// Dashboard is the dashboard's address that the icon opens, for
	// Running and Attention: the loopback address of the access file, never
	// an address from the answer.
	Dashboard string
	// Message and Repair explain Stopped, and Unavailable when the checkup
	// said why; Repair is a command or "".
	Message, Repair string
}

// Diagnosis is what the checkup of this computer says about a server that
// did not answer.
type Diagnosis struct {
	// Stopped is true only when the checkup found that the server does not
	// run.
	Stopped bool
	// Message and Repair are its finding, in the language asked for.
	Message, Repair string
}

// Client reads the status of the server of one state directory, as the
// tray access file there names it.
type Client struct {
	StateDir string
	// Diagnose runs the checkup when the server does not answer.
	Diagnose func(ctx context.Context, lang string) (Diagnosis, error)
	http     *http.Client
	access   *state.TrayAccess
}

// errNoConnection means nothing answered at the address, or the access file
// that names the address could not be read.
var errNoConnection = errors.New("no connection to OwnGit")

// accessFileLimit bounds the tray access file that is read.
const accessFileLimit = 4 << 10

// Timeouts of a status read. Connecting is quick on this computer; the
// answer may wait for the checkup, which runs for up to a minute.
const (
	connectTimeout = 5 * time.Second
	answerTimeout  = 75 * time.Second
)

// NewClient returns a client for the server of stateDir.
func NewClient(stateDir string, diagnose func(context.Context, string) (Diagnosis, error)) *Client {
	transport := &http.Transport{
		// The address is on this computer; a proxy must never see the token.
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: connectTimeout}).DialContext,
		ResponseHeaderTimeout: answerTimeout,
		MaxIdleConns:          1,
		IdleConnTimeout:       30 * time.Second,
	}
	return &Client{StateDir: stateDir, Diagnose: diagnose, http: &http.Client{
		Transport: transport, Timeout: answerTimeout + connectTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Read reads the server's status in lang ("en" or "ko").
func (client *Client) Read(ctx context.Context, lang string) Report {
	status, dashboard, err := client.status(ctx, lang)
	switch {
	case err == nil && status.State == "attention":
		return Report{Condition: Attention, Status: &status, Dashboard: dashboard}
	case err == nil:
		return Report{Condition: Running, Status: &status, Dashboard: dashboard}
	case !errors.Is(err, errNoConnection) || client.Diagnose == nil:
		return Report{Condition: Unavailable}
	}
	diagnosis, err := client.Diagnose(ctx, lang)
	switch {
	case err != nil:
		return Report{Condition: Unavailable}
	case diagnosis.Stopped:
		return Report{Condition: Stopped, Message: diagnosis.Message, Repair: diagnosis.Repair}
	}
	return Report{Condition: Unavailable, Message: diagnosis.Message, Repair: diagnosis.Repair}
}

// status asks the server once, and once more with the access file read
// again when the token is refused. A token is kept only while it works.
func (client *Client) status(ctx context.Context, lang string) (server.TrayStatus, string, error) {
	for attempt := 0; ; attempt++ {
		if client.access == nil {
			access, err := readAccess(filepath.Join(client.StateDir, state.TrayAccessFile))
			if err != nil {
				return server.TrayStatus{}, "", fmt.Errorf("%w: %w", errNoConnection, err)
			}
			client.access = &access
		}
		access := *client.access
		status, code, err := client.ask(ctx, access, lang)
		if err != nil || code != http.StatusOK {
			client.access = nil
		}
		if code == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		return status, strings.TrimSuffix(access.URL, "/"), err
	}
}

// ask sends one status request and checks that the answer is the status
// the contract describes. Anything else is an error.
func (client *Client) ask(ctx context.Context, access state.TrayAccess, lang string) (server.TrayStatus, int, error) {
	address, err := statusURL(access.URL, lang)
	if err != nil {
		return server.TrayStatus{}, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return server.TrayStatus{}, 0, err
	}
	nonce, err := state.NewTrayNonce()
	if err != nil {
		return server.TrayStatus{}, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+access.Token)
	request.Header.Set(state.TrayNonceHeader, nonce)
	request.Header.Set("Accept", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		var operation *net.OpError
		if errors.As(err, &operation) && operation.Op == "dial" {
			return server.TrayStatus{}, 0, fmt.Errorf("%w: %w", errNoConnection, err)
		}
		return server.TrayStatus{}, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return server.TrayStatus{}, response.StatusCode, err
	}
	if response.StatusCode != http.StatusOK {
		return server.TrayStatus{}, response.StatusCode, fmt.Errorf("the status answered %s", response.Status)
	}
	// Nothing of an answer is used before it proves that this server, which
	// holds the secret of the access file, answered this request.
	proof := state.TrayProof(access.Proof, nonce, body)
	if !hmac.Equal([]byte(response.Header.Get(state.TrayProofHeader)), []byte(proof)) {
		return server.TrayStatus{}, response.StatusCode, errors.New("the status answer does not prove that OwnGit sent it")
	}
	if kind, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type")); kind != "application/json" {
		return server.TrayStatus{}, response.StatusCode, errors.New("the status answer is not JSON")
	}
	var status server.TrayStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return server.TrayStatus{}, response.StatusCode, fmt.Errorf("read the status answer: %w", err)
	}
	if !status.OK || status.State != "running" && status.State != "attention" {
		return server.TrayStatus{}, response.StatusCode, errors.New("the status answer is not OwnGit's tray status")
	}
	return status, response.StatusCode, nil
}

// statusURL is the status address of the server at base, which must be a
// plain loopback address as the server writes it.
func statusURL(base, lang string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("the tray access file names %q, which is not a loopback address", base)
	}
	host := parsed.Hostname()
	address, err := netip.ParseAddr(host)
	if (err != nil || !address.IsLoopback()) && !strings.EqualFold(host, "localhost") {
		return "", fmt.Errorf("the tray access file names %q, which is not a loopback address", base)
	}
	return strings.TrimSuffix(base, "/") + server.TrayStatusPath + "?lang=" + url.QueryEscape(lang), nil
}

// readAccess reads the tray access file.
func readAccess(path string) (state.TrayAccess, error) {
	content, err := readSharedFile(path, accessFileLimit)
	if err != nil {
		return state.TrayAccess{}, err
	}
	var access state.TrayAccess
	if err := json.Unmarshal(content, &access); err != nil || access.URL == "" || access.Token == "" || access.Proof == "" {
		return state.TrayAccess{}, fmt.Errorf("%s is not a tray access file", path)
	}
	return access, nil
}

// readLimited reads file, which must hold at most limit bytes.
func readLimited(file io.Reader, path string, limit int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(content)) > limit {
		err = fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return content, err
}
