package firstrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"owngit/internal/tailscale"
)

// TailscaleState is what setup found out about Tailscale on this computer.
type TailscaleState int

const (
	// TailscaleMissing means Tailscale was not found, or its answer could not
	// be read.
	TailscaleMissing TailscaleState = iota
	// TailscaleStopped means the Tailscale command exists but Tailscale is
	// not connected.
	TailscaleStopped
	// TailscaleRunning means Tailscale is connected and has an IPv4 address.
	TailscaleRunning
)

// Tailscale is the result of the read-only detection.
type Tailscale struct {
	State TailscaleState
	// IPv4 is this computer's Tailscale address.
	IPv4 string
	// Name is this computer's MagicDNS name without the trailing dot, or ""
	// when MagicDNS is off.
	Name string
}

// tailscaleTimeout bounds the one status query.
const tailscaleTimeout = 2 * time.Second

// detectTailscale asks Tailscale for its status without changing anything.
// It finds the command the way Tailscale sharing does (override, when not
// empty, is the only path tried) and runs only `tailscale status --json`,
// with a time limit, a minimal environment and no input.
func detectTailscale(ctx context.Context, override string, timeout time.Duration) Tailscale {
	command, err := tailscale.Find(override)
	if err != nil {
		return Tailscale{State: TailscaleMissing}
	}
	return tailscaleStatus(ctx, command.Path, timeout)
}

func tailscaleStatus(ctx context.Context, executable string, timeout time.Duration) Tailscale {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "status", "--json")
	// A minimal environment: the command finds its daemon by its own
	// defaults, and nothing from this process, such as a proxy or a token,
	// is passed on.
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	if home, err := os.UserHomeDir(); err == nil {
		command.Env = append(command.Env, "HOME="+home)
	}
	command.Dir = filepath.Dir(executable)
	command.Stdin = nil
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	var output limitedBuffer
	command.Stdout = &output
	err := command.Run()
	if ctx.Err() != nil {
		return Tailscale{State: TailscaleMissing}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// The command answered but could not report a running Tailscale,
		// for example because its daemon is not running.
		return Tailscale{State: TailscaleStopped}
	}
	if err != nil || output.overflow {
		return Tailscale{State: TailscaleMissing}
	}
	return parseTailscaleStatus(output.Bytes())
}

// parseTailscaleStatus reads the parts of `tailscale status --json` setup
// uses. Anything unexpected means not detected.
func parseTailscaleStatus(content []byte) Tailscale {
	var status struct {
		BackendState string
		Self         *struct {
			DNSName      string
			TailscaleIPs []string
		}
	}
	if err := json.Unmarshal(content, &status); err != nil || status.BackendState == "" {
		return Tailscale{State: TailscaleMissing}
	}
	if status.BackendState != "Running" {
		return Tailscale{State: TailscaleStopped}
	}
	if status.Self == nil {
		return Tailscale{State: TailscaleMissing}
	}
	found := Tailscale{State: TailscaleRunning}
	for _, address := range status.Self.TailscaleIPs {
		if ip := net.ParseIP(address); ip != nil && ip.To4() != nil {
			found.IPv4 = ip.To4().String()
			break
		}
	}
	if found.IPv4 == "" {
		return Tailscale{State: TailscaleMissing}
	}
	if name := strings.TrimSuffix(strings.ToLower(status.Self.DNSName), "."); validHostName(name) {
		found.Name = name
	}
	return found
}

// validHostName accepts a DNS name made of letters, digits and hyphens, so
// nothing else can reach the printed command.
func validHostName(name string) bool {
	if name == "" || len(name) > 253 {
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

// limitedBuffer keeps at most 4 MiB of command output.
type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (buffer *limitedBuffer) Write(p []byte) (int, error) {
	if buffer.Len()+len(p) > 4<<20 {
		buffer.overflow = true
		return len(p), nil
	}
	return buffer.Buffer.Write(p)
}

// tailscaleCommand is the command that saves network settings for the
// Tailscale address, so every later start, a background service included,
// serves OwnGit there. The listen address makes OwnGit accept the Tailscale
// IP address and the base URL its MagicDNS name, when there is one. It is
// printed for the owner to copy; OwnGit never runs it and saves nothing.
// Every part is quoted by shellQuote, so the command holds no control or
// direction characters and can be printed as it is.
func tailscaleCommand(found Tailscale, port, stateDir string) string {
	address := net.JoinHostPort(found.IPv4, port)
	host := found.Name
	if host == "" {
		host = found.IPv4
	}
	parts := []string{"owngit", "network", "set", "--listen", address, "--base-url", "http://" + net.JoinHostPort(host, port)}
	if stateDir != "" {
		parts = append(parts, "--state-dir", stateDir)
	}
	for i, part := range parts {
		parts[i] = shellQuote(part)
	}
	return strings.Join(parts, " ")
}

// localOnlyCommand is the command that saves a listen address only this
// computer can reach, on the same port, and removes the saved base URL, which
// would name an address other devices use. Like tailscaleCommand it is
// printed for the owner and never run.
func localOnlyCommand(port, stateDir string) string {
	parts := []string{"owngit", "network", "set", "--listen", net.JoinHostPort("127.0.0.1", port), "--base-url", ""}
	if stateDir != "" {
		parts = append(parts, "--state-dir", stateDir)
	}
	for i, part := range parts {
		parts[i] = shellQuote(part)
	}
	return strings.Join(parts, " ")
}

// shellQuote quotes a value for a shell when it needs quoting. A value with
// a control or direction character, or with bytes that are not UTF-8, is
// written in ANSI-C quotes ($'...', read by zsh, bash and ksh) with those
// bytes as \xHH escapes, so the copied command names exactly this value.
func shellQuote(value string) string {
	safe, plain := value != "", utf8.ValidString(value)
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._-+:,@%=", c)) {
			safe = false
		}
		if unshowable(c) {
			plain = false
		}
	}
	switch {
	case safe:
		return value
	case plain:
		return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
	}
	var out strings.Builder
	out.WriteString("$'")
	for i := 0; i < len(value); {
		r, size := utf8.DecodeRuneInString(value[i:])
		switch {
		case r == utf8.RuneError && size <= 1, unshowable(r):
			for _, b := range []byte(value[i : i+size]) {
				fmt.Fprintf(&out, `\x%02x`, b)
			}
		case r == '\\' || r == '\'':
			out.WriteByte('\\')
			out.WriteRune(r)
		default:
			out.WriteString(value[i : i+size])
		}
		i += size
	}
	out.WriteByte('\'')
	return out.String()
}
