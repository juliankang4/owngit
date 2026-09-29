// Package tailscale lets OwnGit be shared on the owner's tailnet over HTTPS
// with Tailscale Serve.
//
// It reads Tailscale's status with the host's tailscale command, and reads
// and changes the Serve configuration through Tailscale's LocalAPI
// (localapi.go), where a change applies only to the configuration it was
// made from. It adds or removes exactly one HTTPS endpoint and keeps
// everything else in the configuration as Tailscale gave it. It never turns
// on Funnel and never runs up, down, set, cert or sudo. Every command passes
// an argument array without a shell, and every call has a time limit.
package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

// Time limits of one call to Tailscale. A write can wait for the daemon to
// fetch a certificate, so it gets longer.
const (
	readTimeout  = 10 * time.Second
	writeTimeout = 30 * time.Second
)

// maxOutput bounds what one command may print or one LocalAPI answer hold.
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
	// LocalAPI connects to the LocalAPI of the Tailscale that Path talks
	// to, which Serve is read and changed through. Find sets it for this
	// computer; without it, Serve can be neither read nor changed.
	LocalAPI Dialer
}

// Paths that Find and the macOS LocalAPI dialer inspect. Variables only so
// tests can point them at synthetic files.
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
			mac, dial := localAPIFor(path)
			return Command{Path: path, MacApp: mac, LocalAPI: dial}, nil
		}
	}
	return Command{}, ErrNotInstalled
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
	// KindHTTPSUnavailable: the tailnet's control server is not
	// Tailscale's, such as Headscale, and gives this computer no
	// certificate name, so no admin console setting can turn HTTPS on.
	KindHTTPSUnavailable Kind = "https_unavailable"
	// KindPermission: the daemon refused a change for this user, such as a
	// user who is not the operator on Linux.
	KindPermission Kind = "permission"
	// KindMacAppAdmin: the Standalone Tailscale app for macOS lets only
	// administrator accounts reach its LocalAPI, and this account is not
	// one.
	KindMacAppAdmin Kind = "mac_app_admin"
	// KindOutdated: the daemon is older than Tailscale 1.50 and cannot
	// apply a change only to the Serve configuration it was made from.
	KindOutdated Kind = "outdated"
	// KindServeChanged: the Serve configuration changed after it was read,
	// so the change made from it was not applied.
	KindServeChanged Kind = "serve_changed"
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
	// Detail is what Tailscale printed or answered on failure, shortened,
	// for KindFailed and KindPermission; never OwnGit's own words.
	Detail string
	// cause is the connection's error when an exchange with Tailscale
	// failed without an answer. Error includes it for logs; it is not
	// Tailscale's words, so it is never in Detail.
	cause error
}

func (err *Error) Error() string {
	text := "tailscale: " + string(err.Kind)
	if err.Detail != "" {
		text += ": " + err.Detail
	}
	if err.cause != nil {
		text += " (" + err.cause.Error() + ")"
	}
	return text
}

// Unwrap returns the connection's error behind a failed exchange, or nil.
func (err *Error) Unwrap() error {
	return err.cause
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
	// Addresses are this computer's own Tailscale IP addresses.
	Addresses []netip.Addr
}

// tailnetRanges are the address ranges Tailscale gives its devices: the
// shared address space for IPv4 and Tailscale's own IPv6 prefix.
var tailnetRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// InTailnetRange reports whether addr is in the ranges Tailscale gives its
// devices. Other networks, such as NetBird or a carrier's NAT, can use the
// IPv4 range too, so this alone does not show that Tailscale carried a
// connection.
func InTailnetRange(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range tailnetRanges {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// suffixTailscale is the end of every MagicDNS name that Tailscale's own
// control server gives out. Another control server, such as Headscale,
// uses a domain of its own.
const suffixTailscale = ".ts.net"

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
	if !strings.HasSuffix(status.Name, suffixTailscale) {
		return &Error{Kind: KindHTTPSUnavailable}
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
		Self         *struct {
			DNSName      string
			TailscaleIPs []string
		}
		TailscaleIPs []string
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
	addresses := raw.TailscaleIPs
	if raw.Self != nil {
		status.Name = strings.ToLower(strings.TrimSuffix(raw.Self.DNSName, "."))
		if len(raw.Self.TailscaleIPs) > 0 {
			addresses = raw.Self.TailscaleIPs
		}
	}
	for _, text := range addresses {
		if addr, err := netip.ParseAddr(text); err == nil && InTailnetRange(addr) {
			status.Addresses = append(status.Addresses, addr.Unmap())
		}
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

// ServeConfig reads the Serve configuration and its version, which a change
// made from it binds to. A daemon that gives no version cannot bind a
// change, and the read fails with KindOutdated.
func (command Command) ServeConfig(ctx context.Context) (ServeConfig, error) {
	version, content, err := command.serveConfig(ctx, readTimeout, "", nil)
	if err != nil {
		return ServeConfig{}, err
	}
	if version == "" {
		return ServeConfig{}, &Error{Kind: KindOutdated}
	}
	config, err := ParseServeConfig(content)
	if err != nil {
		return ServeConfig{}, err
	}
	config.version, config.content = version, content
	return config, nil
}

// ServeHTTPS adds a background Serve endpoint to read, a configuration
// ServeConfig returned: for name on port, answering HTTPS at path "/" and
// proxying to target, such as "http://127.0.0.1:7654". Tailscale applies it
// only while its configuration is still read, and otherwise answers
// KindServeChanged. It never enables Funnel. Callers read the configuration
// back afterwards: Tailscale can accept a change without keeping it.
func (command Command) ServeHTTPS(ctx context.Context, read ServeConfig, name string, port int, target string) error {
	return command.change(ctx, read, name, port, target)
}

// RemoveHTTPS removes from read the endpoint for name on port, which the
// caller found to be exactly OwnGit's. It applies as ServeHTTPS does.
func (command Command) RemoveHTTPS(ctx context.Context, read ServeConfig, name string, port int) error {
	return command.change(ctx, read, name, port, "")
}

// change writes read with the endpoint for name on port set to target, or
// removed when target is empty, bound to the version of read.
func (command Command) change(ctx context.Context, read ServeConfig, name string, port int, target string) error {
	if read.version == "" {
		// Not read from Tailscale: there is nothing to bind the change to.
		return &Error{Kind: KindOutdated}
	}
	content, err := read.withEndpoint(name, port, target)
	if err != nil {
		return err
	}
	_, _, err = command.serveConfig(ctx, writeTimeout, read.version, content)
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
