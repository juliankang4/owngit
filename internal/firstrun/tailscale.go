package firstrun

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"owngit/internal/tailscale"
)

// TailscaleState is what setup found out about Tailscale on this computer.
type TailscaleState int

const (
	// TailscaleMissing means the tailscale command was not found, or
	// Tailscale gives this computer no IPv4 address.
	TailscaleMissing TailscaleState = iota
	// TailscaleUnusable means Tailscale was found but cannot be used now,
	// for the reason in Problem.
	TailscaleUnusable
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
	// Problem is why Tailscale cannot be used, as sharing on the tailnet
	// names it (a tailscale.Kind), such as "logged_out" or
	// "untrusted_socket", so setup gives the same guidance.
	Problem string
}

// tailscaleTimeout bounds the one status query.
const tailscaleTimeout = 2 * time.Second

// detectTailscale asks Tailscale for its status without changing anything.
// It finds Tailscale the way Tailscale sharing does (override, when not
// empty, is the only tailscale command tried) and reads its status as
// sharing does, from Tailscale's own service (tailscale.Command.Status).
func detectTailscale(ctx context.Context, override string, timeout time.Duration) Tailscale {
	command, err := tailscale.Find(override)
	if err != nil {
		return Tailscale{State: TailscaleMissing}
	}
	return readTailscale(ctx, command, timeout)
}

// readTailscale reads the status of the Tailscale that command reaches.
// When the status cannot be read, or Tailscale is not connected, the
// Problem is what sharing on the tailnet reports then. Setup needs no
// MagicDNS or HTTPS certificates, so a connected Tailscale is used without
// sharing's further checks.
func readTailscale(ctx context.Context, command tailscale.Command, timeout time.Duration) Tailscale {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	status, err := command.Status(ctx)
	if err == nil && status.BackendState != "Running" {
		err = status.Usable()
	}
	if err != nil {
		return Tailscale{State: TailscaleUnusable, Problem: string(tailscale.KindOf(err))}
	}
	found := Tailscale{State: TailscaleRunning, Name: status.Name}
	for _, address := range status.Addresses {
		if address.Is4() {
			found.IPv4 = address.String()
			break
		}
	}
	if found.IPv4 == "" {
		return Tailscale{State: TailscaleMissing}
	}
	return found
}

// tailscaleCommand is the command that saves network settings for the
// Tailscale address, so every later start, a background service included,
// serves OwnGit there. The listen address makes OwnGit accept the Tailscale
// IP address and the base URL its MagicDNS name, when there is one. It is
// printed for the owner to copy; OwnGit never runs it and saves nothing.
// Every part is quoted by shellQuote, so the command holds no control or
// direction characters and can be printed as it is.
func tailscaleCommand(command string, found Tailscale, port, stateDir string) string {
	address := net.JoinHostPort(found.IPv4, port)
	host := found.Name
	if host == "" {
		host = found.IPv4
	}
	return networkSetCommand(command, stateDir, "--listen", address, "--base-url", "http://"+net.JoinHostPort(host, port))
}

// localOnlyCommand is the command that saves a listen address only this
// computer can reach, on the same port, and removes the saved base URL, which
// would name an address other devices use. Like tailscaleCommand it is
// printed for the owner and never run.
func localOnlyCommand(command, port, stateDir string) string {
	// "--base-url=" rather than an empty argument, which Windows PowerShell
	// 5.1 drops.
	return networkSetCommand(command, stateDir, "--listen", net.JoinHostPort("127.0.0.1", port), "--base-url=")
}

// networkSetCommand is "network set" with options, run by command (see
// commandWord) and with the state folder when it is not the default.
func networkSetCommand(command, stateDir string, options ...string) string {
	parts := append([]string{"network", "set"}, options...)
	if stateDir != "" {
		parts = append(parts, "--state-dir", stateDir)
	}
	for i, part := range parts {
		parts[i] = shellQuote(part)
	}
	return command + " " + strings.Join(parts, " ")
}

// commandWord is how a printed command starts this OwnGit: "owngit" when
// that name on PATH is this executable, and otherwise this executable's
// path, as for a copy from an archive that is not on PATH. Without the path
// of this executable, "owngit" is the best hint left.
func commandWord() string {
	self, err := os.Executable()
	if err != nil {
		return "owngit"
	}
	if onPath, err := exec.LookPath("owngit"); err == nil && sameFile(onPath, self) {
		return "owngit"
	}
	quoted := shellQuote(self)
	if runtime.GOOS == "windows" && quoted != self {
		// PowerShell runs a quoted path only after its call operator.
		return "& '" + strings.ReplaceAll(self, "'", "''") + "'"
	}
	return quoted
}

// sameFile reports whether the paths name the same file.
func sameFile(first, second string) bool {
	a, err := os.Stat(first)
	if err != nil {
		return false
	}
	b, err := os.Stat(second)
	return err == nil && os.SameFile(a, b)
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
