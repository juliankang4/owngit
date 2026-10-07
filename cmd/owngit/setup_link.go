package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"

	"owngit/internal/bootstrap"
	"owngit/internal/firstrun"
	"owngit/internal/server"
	"owngit/internal/state"
)

// The one-time setup link is shown on the owner's terminal only. Written
// anywhere else, such as a pipe, a log file, the journal or "docker logs",
// it would outlive the moment and reach other readers, so there the output
// names the owner-only setup file instead, which holds the same link.

// stdoutIsTerminal reports whether standard output is a terminal. Tests
// replace it.
var stdoutIsTerminal = func() bool { return firstrun.IsTerminal(os.Stdout) }

func setupLink(arguments []string) error {
	flags := flag.NewFlagSet("setup-link", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	stateDir := flags.String("state-dir", defaultStateDir(), "host-local state directory")
	baseURL := flags.String("base-url", "", "owner-facing HTTP origin of the link (default: the addresses the running server listens on)")
	noOpen := flags.Bool("no-open", false, "do not open the private setup file")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("setup-link takes no positional arguments")
	}
	ctx := context.Background()
	store, err := openLiveState(ctx, *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	settings, err := store.Settings(ctx)
	if err != nil {
		return err
	}
	if settings.Initialized {
		return errors.New("setup is already complete")
	}
	path, err := issueAndShowSetupLink(ctx, store, *baseURL, os.Stdout, stdoutIsTerminal())
	if err != nil {
		return err
	}
	if !*noOpen && probeEnvironment().ShowsBrowser() {
		return bootstrap.Open(path)
	}
	return nil
}

// issueAndShowSetupLink issues a new setup link, which replaces the one
// before, and returns the setup file path. On a terminal it shows the link
// for each address of this computer that the server can be reached by, the
// most likely first. Elsewhere it shows only the setup file path.
func issueAndShowSetupLink(ctx context.Context, store *state.Store, baseURL string, out io.Writer, terminal bool) (string, error) {
	bases, tunnel, container := []string{baseURL}, "", false
	if baseURL == "" {
		var err error
		if container, err = inContainerImage(); err != nil {
			return "", err
		}
		if bases, tunnel, err = setupBases(ctx, store); err != nil {
			return "", err
		}
	}
	path, capability, err := (&bootstrap.Issuer{Store: store, BaseURL: bases[0]}).IssueLink(ctx)
	if err != nil {
		return "", err
	}
	if !terminal {
		fmt.Fprintf(out, "Owner setup file written to %s\n", printable(path))
		fmt.Fprintln(out, "Run \"owngit setup-link\" in a terminal to see the setup link there.")
		return path, nil
	}
	switch {
	case tunnel != "":
		fmt.Fprintln(out, "This computer has no address on a private network or a tailnet. The setup link is not shown for a public address, where it and your passwords would cross the Internet unencrypted. On your own computer, open an SSH tunnel:")
		fmt.Fprintf(out, "  %s\n", printable(tunnel))
		fmt.Fprintln(out, "Keep it open, and open this one-time setup link there. It works once, within 15 minutes:")
	case container:
		fmt.Fprintln(out, "Open this one-time setup link in a browser on the computer that runs the OwnGit container. From another device, use that computer's name or address in place of localhost. It works once, within 15 minutes:")
	case len(bases) == 1:
		fmt.Fprintln(out, "Open this one-time setup link in a browser. It works once, within 15 minutes:")
	default:
		fmt.Fprintln(out, "Open this one-time setup link in a browser, with an address of this computer that your device reaches (the most likely first). It works once, within 15 minutes:")
	}
	for _, base := range bases {
		link, err := bootstrap.SetupLink(base, capability)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(out, "  %s\n", printable(link))
	}
	fmt.Fprintf(out, "Whoever opens it first can set up OwnGit, so do not share it. The same link is in %s, and \"owngit setup-link\" makes a new one.\n", printable(path))
	return path, nil
}

// setupBases returns the origins a setup link can use: the saved or
// running base URL, then the listen address, where listening on every
// address means this computer's private and tailnet addresses. A computer
// with a screen lists 127.0.0.1 before them. A computer without a screen
// and without such an address gets the link on 127.0.0.1 and, as tunnel,
// the SSH command that forwards it from the owner's own computer. In the
// container image the link names localhost, which a browser on the
// computer that runs the container opens through the published port.
func setupBases(ctx context.Context, store *state.Store) (bases []string, tunnel string, err error) {
	observed, err := store.ObserveRunningNetwork(ctx)
	if err != nil {
		return nil, "", err
	}
	var listen, base string
	if observed.Server == state.ServerRunning && observed.Record != nil {
		listen, base = observed.Record.Address, observed.Record.BaseURL
	} else {
		saved, err := store.NetworkSettings(ctx)
		if err != nil {
			return nil, "", err
		}
		listen, base = cmp.Or(saved.Listen, server.DefaultListenAddress), saved.BaseURL
	}
	if base != "" {
		bases = append(bases, base)
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, "", fmt.Errorf("listen address %q: %w", listen, err)
	}
	loopback := "http://" + net.JoinHostPort("127.0.0.1", port)
	if ip, err := netip.ParseAddr(host); host != "" && (err != nil || !ip.IsUnspecified()) {
		return append(bases, "http://"+net.JoinHostPort(host, port)), "", nil
	}
	if container, err := inContainerImage(); err != nil {
		return nil, "", err
	} else if container {
		// The container's own addresses are on a network that only the
		// computer running it reaches, and the address that other devices
		// use is that computer's, which the container cannot see.
		return append(bases, "http://"+net.JoinHostPort("localhost", port)), "", nil
	}
	headless := probeEnvironment().Headless()
	if !headless {
		bases = append(bases, loopback)
	}
	private := lanAddresses()
	for _, address := range private {
		bases = append(bases, "http://"+net.JoinHostPort(address.String(), port))
	}
	if headless && len(private) == 0 {
		bases = append(bases, loopback)
		tunnel = sshTunnelCommand(port)
	}
	return slices.Compact(bases), tunnel, nil
}

// sshTunnelCommand is the command that forwards port on the owner's own
// computer to this computer's loopback. It names the address and account
// of the current SSH session when there is one.
func sshTunnelCommand(port string) string {
	host := ""
	if fields := strings.Fields(os.Getenv("SSH_CONNECTION")); len(fields) >= 3 {
		host = fields[2]
	}
	if host == "" {
		if address, ok := defaultRouteAddress(); ok {
			host = address.String()
		}
	}
	if host == "" {
		host, _ = os.Hostname()
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	account := cmp.Or(os.Getenv("SUDO_USER"), os.Getenv("USER"), os.Getenv("LOGNAME"), "USER")
	return fmt.Sprintf("ssh -L %s:127.0.0.1:%s %s@%s", port, port, account, cmp.Or(host, "HOST"))
}

// printable replaces control and text direction characters, which could
// rewrite what a terminal shows, with "?".
func printable(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x20, r >= 0x7f && r < 0xa0, r >= 0x200e && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return '?'
		}
		return r
	}, text)
}

// interfaceAddress is one address of a network interface.
type interfaceAddress struct {
	name    string
	address netip.Addr
}

// lanAddresses lists the addresses other devices on a private network may
// reach this computer by, the most likely first: the address of the default
// route when it is private, then private IPv4, tailnet (100.64.0.0/10) and
// unique local IPv6 addresses. Public addresses are left out, so a setup
// link never sends its capability and the first passwords unencrypted over
// the Internet. Container and virtual bridges are left out too.
var lanAddresses = func() []netip.Addr {
	preferred, _ := defaultRouteAddress()
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var candidates []interfaceAddress
	for _, entry := range interfaces {
		if entry.Flags&net.FlagUp == 0 || entry.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := entry.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			if network, ok := address.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(network.IP); ok {
					candidates = append(candidates, interfaceAddress{name: entry.Name, address: ip.Unmap()})
				}
			}
		}
	}
	return orderAddresses(preferred, candidates)
}

// defaultRouteAddress is the source address of the default IPv4 route.
// Connecting a UDP socket sends nothing; it only selects that address.
func defaultRouteAddress() (netip.Addr, bool) {
	connection, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return netip.Addr{}, false
	}
	defer connection.Close()
	local, ok := connection.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	address, ok := netip.AddrFromSlice(local.IP)
	return address.Unmap(), ok
}

// maxSetupAddresses bounds how many addresses a setup link is shown for.
const maxSetupAddresses = 6

// tailnetPrefix is the shared address space (RFC 6598) that Tailscale uses.
var tailnetPrefix = netip.MustParsePrefix("100.64.0.0/10")

// privateAddress reports whether address belongs to a private network: a
// private IPv4 range, the tailnet range, or unique local IPv6.
func privateAddress(address netip.Addr) bool {
	return address.IsPrivate() || tailnetPrefix.Contains(address)
}

func orderAddresses(preferred netip.Addr, candidates []interfaceAddress) []netip.Addr {
	rank := func(candidate interfaceAddress) int {
		address := candidate.address
		switch {
		case address == preferred:
			return 0
		case address.Is4() && address.IsPrivate():
			return 1
		case address.Is4():
			return 2
		default:
			return 3
		}
	}
	var kept []interfaceAddress
	for _, candidate := range candidates {
		if !privateAddress(candidate.address) || virtualInterface(candidate.name) {
			continue
		}
		kept = append(kept, candidate)
	}
	slices.SortStableFunc(kept, func(left, right interfaceAddress) int { return cmp.Compare(rank(left), rank(right)) })
	var ordered []netip.Addr
	for _, candidate := range kept {
		if !slices.Contains(ordered, candidate.address) && len(ordered) < maxSetupAddresses {
			ordered = append(ordered, candidate.address)
		}
	}
	return ordered
}

// virtualInterface recognizes the bridges and links that container and
// virtual machine tools create; other devices do not reach them.
func virtualInterface(name string) bool {
	for _, prefix := range []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "podman", "lxcbr", "lxdbr"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// headlessListen is server.HeadlessListenAddress; tests use a free port on
// loopback instead.
var headlessListen = server.HeadlessListenAddress

// headlessChoice is the value of serve's --headless flag when it was given,
// and otherwise whether this computer looks like it has no screen.
func headlessChoice(flags *flag.FlagSet, value bool) bool {
	given := false
	flags.Visit(func(entry *flag.Flag) { given = given || entry.Name == "headless" })
	if given {
		return value
	}
	return probeEnvironment().Headless()
}

// headlessListenInUse returns the every-address listen address when this
// start before setup on a computer without a screen uses the one the
// headless rule saves, and "" otherwise.
func headlessListenInUse(headlessSetup bool, network serveNetwork) string {
	if headlessSetup && network.ListenSource == sourceSaved && network.Listen == headlessListen {
		return network.Listen
	}
	return ""
}

// applyHeadlessListen saves the every-address listen address for an
// installation that is not set up and has no saved listen address. It
// reports whether it saved it.
func applyHeadlessListen(ctx context.Context, store *state.Store) (bool, error) {
	settings, err := store.Settings(ctx)
	if err != nil || settings.Initialized {
		return false, err
	}
	saved, err := store.NetworkSettings(ctx)
	if err != nil || saved.Listen != "" {
		return false, err
	}
	saved.Listen = headlessListen
	return true, store.UpdateNetwork(ctx, state.NetworkUpdate{Settings: saved})
}

// saveHeadlessListen is applyHeadlessListen for a state directory.
func saveHeadlessListen(stateDir string) error {
	ctx := context.Background()
	store, err := openLiveState(ctx, stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	_, err = applyHeadlessListen(ctx, store)
	return err
}
