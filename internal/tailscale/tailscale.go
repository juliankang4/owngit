// Package tailscale runs the host's tailscale command so OwnGit can be
// shared on the owner's tailnet over HTTPS with Tailscale Serve.
//
// It reads Tailscale's status and Serve configuration, and it writes or
// removes exactly one HTTPS endpoint. It never runs funnel, "serve reset",
// up, down, set, cert or sudo. Every call passes an argument array without a
// shell and has a time limit.
package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Time limits of one tailscale command. A write can wait for the daemon to
// fetch a certificate, so it gets longer.
const (
	readTimeout  = 10 * time.Second
	writeTimeout = 30 * time.Second
)

// maxOutput bounds what one command may print.
const maxOutput = 4 << 20

// Command is a tailscale executable found on this computer.
type Command struct {
	// Path is the executable.
	Path string
	// MacApp is true when Path runs the Tailscale app for macOS (the App
	// Store or Standalone variant) instead of the open source tailscaled.
	// That app does not run until someone logs in to the Mac, so the HTTPS
	// address stops working after a restart until then.
	MacApp bool
}

// Paths that Find and macApp inspect. Variables only so tests can point
// them at synthetic files.
var (
	macAppBundle     = "/Applications/Tailscale.app"
	openSourceSocket = "/var/run/tailscaled.socket"
)

// candidates lists where the tailscale command may be, in order: on PATH,
// then where the installers put it, since a service manager often starts
// OwnGit with a short PATH.
func candidates() []string {
	var paths []string
	if path, err := exec.LookPath("tailscale"); err == nil {
		paths = append(paths, path)
	}
	switch runtime.GOOS {
	case "darwin":
		paths = append(paths, "/opt/homebrew/bin/tailscale", "/usr/local/bin/tailscale",
			filepath.Join(macAppBundle, "Contents", "MacOS", "Tailscale"))
	case "linux":
		paths = append(paths, "/usr/bin/tailscale", "/usr/local/bin/tailscale", "/usr/sbin/tailscale")
	case "windows":
		if programs := os.Getenv("ProgramFiles"); programs != "" {
			paths = append(paths, filepath.Join(programs, "Tailscale", "tailscale.exe"))
		}
	}
	return paths
}

// ErrNotInstalled means no tailscale command was found.
var ErrNotInstalled = &Error{Kind: KindNotInstalled}

// Find returns the tailscale command. override, when not empty, is the only
// path tried, such as the value of a --tailscale option.
func Find(override string) (Command, error) {
	paths := candidates()
	if override != "" {
		paths = []string{override}
	}
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return Command{Path: path, MacApp: macApp(path)}, nil
		}
	}
	return Command{}, ErrNotInstalled
}

// macApp reports whether path runs the Tailscale app for macOS. The app's
// command is its bundle executable, or a launcher that the Standalone app
// installs outside the bundle; the open source tailscaled instead listens on
// a Unix socket that the app never creates.
func macApp(path string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if strings.Contains(path, ".app/Contents/") {
		return true
	}
	if _, err := os.Stat(openSourceSocket); err == nil {
		return false
	}
	_, err := os.Stat(macAppBundle)
	return err == nil
}

// Kind classifies a problem so the owner gets a message with its fix.
type Kind string

const (
	KindNotInstalled Kind = "not_installed"
	// KindNotRunning: the command exists but its daemon does not answer.
	KindNotRunning Kind = "not_running"
	// KindLoggedOut: Tailscale on this computer is not signed in.
	KindLoggedOut Kind = "logged_out"
	// KindStopped: Tailscale is signed in but turned off.
	KindStopped Kind = "stopped"
	// KindNeedsApproval: the tailnet administrator has to approve this
	// computer.
	KindNeedsApproval Kind = "needs_approval"
	// KindMagicDNSOff: MagicDNS is off, so this computer has no name.
	KindMagicDNSOff Kind = "magicdns_off"
	// KindHTTPSOff: HTTPS certificates are not enabled in the tailnet.
	KindHTTPSOff Kind = "https_off"
	// KindPermission: the daemon refused a change for this user, such as a
	// user who is not the operator on Linux.
	KindPermission Kind = "permission"
	// KindTimeout: the command did not finish in time.
	KindTimeout Kind = "timeout"
	// KindUnreadable: the command printed something this code cannot read.
	KindUnreadable Kind = "unreadable"
	// KindFailed: the command failed for another reason; Detail says what
	// it printed.
	KindFailed Kind = "failed"
)

// Error is a failed tailscale call or an unusable Tailscale state.
type Error struct {
	Kind Kind
	// Detail is what the command printed on failure, shortened, for
	// KindFailed and KindPermission.
	Detail string
}

func (err *Error) Error() string {
	if err.Detail != "" {
		return "tailscale: " + string(err.Kind) + ": " + err.Detail
	}
	return "tailscale: " + string(err.Kind)
}

// KindOf returns the Kind of err, or KindFailed for another error.
func KindOf(err error) Kind {
	var failure *Error
	if errors.As(err, &failure) {
		return failure.Kind
	}
	return KindFailed
}

// Status is the part of "tailscale status --json" OwnGit uses.
type Status struct {
	// BackendState is Tailscale's state, such as "Running" or "NeedsLogin".
	BackendState string
	// Name is this computer's MagicDNS name without the trailing dot, in
	// lower case, such as "box.tail1234.ts.net".
	Name string
	// MagicDNS is whether the tailnet has MagicDNS on.
	MagicDNS bool
	// CertDomains are the names Tailscale can get certificates for; empty
	// when HTTPS certificates are not enabled in the tailnet.
	CertDomains []string
	// Version is the daemon's version.
	Version string
}

// Usable returns nil when Tailscale can serve this computer over HTTPS, or
// an Error that names what is missing.
func (status Status) Usable() error {
	switch status.BackendState {
	case "Running":
	case "NeedsLogin", "NoState":
		return &Error{Kind: KindLoggedOut}
	case "Stopped":
		return &Error{Kind: KindStopped}
	case "NeedsMachineAuth":
		return &Error{Kind: KindNeedsApproval}
	case "Starting":
		return &Error{Kind: KindNotRunning}
	default:
		return &Error{Kind: KindFailed, Detail: "state " + status.BackendState}
	}
	if !status.MagicDNS || status.Name == "" {
		return &Error{Kind: KindMagicDNSOff}
	}
	for _, domain := range status.CertDomains {
		if strings.EqualFold(strings.TrimSuffix(domain, "."), status.Name) {
			return nil
		}
	}
	return &Error{Kind: KindHTTPSOff}
}

// Status runs "tailscale status --json".
func (command Command) Status(ctx context.Context) (Status, error) {
	output, err := command.run(ctx, readTimeout, "status", "--json")
	// A logged-out client may exit with an error and still print its state.
	var raw struct {
		BackendState string
		Version      string
		Self         *struct{ DNSName string }
		CertDomains  []string
		// CurrentTailnet is missing from old clients, which then report
		// MagicDNS through the legacy suffix only.
		CurrentTailnet *struct{ MagicDNSEnabled bool }
		MagicDNSSuffix string
	}
	if decodeErr := json.Unmarshal(output, &raw); decodeErr != nil || raw.BackendState == "" {
		if err != nil {
			return Status{}, err
		}
		return Status{}, &Error{Kind: KindUnreadable}
	}
	status := Status{BackendState: raw.BackendState, Version: raw.Version, CertDomains: raw.CertDomains}
	if raw.Self != nil {
		status.Name = strings.ToLower(strings.TrimSuffix(raw.Self.DNSName, "."))
	}
	if raw.CurrentTailnet != nil {
		status.MagicDNS = raw.CurrentTailnet.MagicDNSEnabled
	} else {
		status.MagicDNS = raw.MagicDNSSuffix != ""
	}
	if !validName(status.Name) {
		status.Name = ""
	}
	return status, nil
}

// ServeConfig runs "tailscale serve status --json".
func (command Command) ServeConfig(ctx context.Context) (ServeConfig, error) {
	output, err := command.run(ctx, readTimeout, "serve", "status", "--json")
	if err != nil {
		return ServeConfig{}, err
	}
	return ParseServeConfig(output)
}

// ServeHTTPS adds a background Serve endpoint that answers HTTPS on port
// and proxies to target, such as "http://127.0.0.1:7654", at path "/". It
// never enables Funnel. Callers read the configuration back afterwards: the
// command can succeed without the change being kept.
func (command Command) ServeHTTPS(ctx context.Context, port int, target string) error {
	_, err := command.run(ctx, writeTimeout, "serve", "--bg", "--https="+strconv.Itoa(port), target)
	return err
}

// RemoveHTTPS removes the handler at path "/" of the HTTPS endpoint on port,
// and nothing else.
func (command Command) RemoveHTTPS(ctx context.Context, port int) error {
	_, err := command.run(ctx, writeTimeout, "serve", "--https="+strconv.Itoa(port), "--set-path=/", "off")
	return err
}

// run runs one command with a time limit and no input. The environment is
// OwnGit's own, so the owner's settings for their tailscale apply, plus
// TAILSCALE_BE_CLI, which makes the macOS app executable act as the command
// line tool when it is started by a program instead of a terminal.
func (command Command) run(ctx context.Context, timeout time.Duration, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	process := exec.CommandContext(ctx, command.Path, arguments...)
	process.Env = append(os.Environ(), "TAILSCALE_BE_CLI=1")
	process.Stdin = nil
	process.WaitDelay = 2 * time.Second
	var stdout, stderr limitedBuffer
	process.Stdout, process.Stderr = &stdout, &stderr
	err := process.Run()
	switch {
	case ctx.Err() != nil:
		return stdout.Bytes(), &Error{Kind: KindTimeout}
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return nil, ErrNotInstalled
	case stdout.overflow:
		return nil, &Error{Kind: KindUnreadable}
	case err != nil:
		return stdout.Bytes(), classify(stderr.String() + "\n" + stdout.String())
	}
	return stdout.Bytes(), nil
}

// classify turns what a failed command printed into an Error.
func classify(output string) error {
	lower := strings.ToLower(output)
	detail := shorten(output)
	switch {
	case strings.Contains(lower, "access denied"), strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "operation not permitted"), strings.Contains(lower, "must be root"):
		return &Error{Kind: KindPermission, Detail: detail}
	case strings.Contains(lower, "failed to connect to local tailscale"), strings.Contains(lower, "doesn't appear to be running"),
		strings.Contains(lower, "is tailscaled running"), strings.Contains(lower, "tailscaled not running"):
		return &Error{Kind: KindNotRunning}
	case strings.Contains(lower, "logged out"), strings.Contains(lower, "needslogin"):
		return &Error{Kind: KindLoggedOut}
	}
	return &Error{Kind: KindFailed, Detail: detail}
}

// shorten keeps the first 300 printable characters of output on one line.
func shorten(output string) string {
	fields := strings.FieldsFunc(output, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) })
	text := strings.Join(fields, " ")
	if runes := []rune(text); len(runes) > 300 {
		text = string(runes[:300]) + "..."
	}
	return text
}

// validName accepts a DNS name made of letters, digits and hyphens, so
// nothing else reaches settings, commands or pages.
func validName(name string) bool {
	if name == "" || len(name) > 253 || !strings.Contains(name, ".") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// limitedBuffer keeps at most maxOutput bytes.
type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (buffer *limitedBuffer) Write(p []byte) (int, error) {
	if buffer.Len()+len(p) > maxOutput {
		buffer.overflow = true
		return len(p), nil
	}
	return buffer.Buffer.Write(p)
}

// Target is the Serve proxy target for OwnGit listening on port: Tailscale
// Serve proxies only to 127.0.0.1.
func Target(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}
