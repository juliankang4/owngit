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
	if !*noOpen && !probeEnvironment().Headless() {
		return bootstrap.Open(path)
	}
	return nil
}

// issueAndShowSetupLink issues a new setup link, which replaces the one
// before, and returns the setup file path. On a terminal it shows the link
// for each address of this computer that the server can be reached by, the
// most likely first. Elsewhere it shows only the setup file path.
func issueAndShowSetupLink(ctx context.Context, store *state.Store, baseURL string, out io.Writer, terminal bool) (string, error) {
	bases := []string{baseURL}
	if baseURL == "" {
		var err error
		if bases, err = setupBases(ctx, store); err != nil {
			return "", err
		}
	}
	path, capability, err := (&bootstrap.Issuer{Store: store, BaseURL: bases[0]}).IssueLink(ctx)
	if err != nil {
		return "", err
	}
	if !terminal {
		fmt.Fprintf(out, "Owner setup file written to %s\n", path)
		fmt.Fprintln(out, "Run \"owngit setup-link\" in a terminal to see the setup link there.")
		return path, nil
	}
	if len(bases) == 1 {
		fmt.Fprintln(out, "Open this one-time setup link in a browser. It works once, within 15 minutes:")
	} else {
		fmt.Fprintln(out, "Open this one-time setup link in a browser, with an address of this computer that your device reaches (the most likely first). It works once, within 15 minutes:")
	}
	for _, base := range bases {
		link, err := bootstrap.SetupLink(base, capability)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(out, "  %s\n", link)
	}
	fmt.Fprintf(out, "Whoever opens it first can set up OwnGit, so do not share it. The same link is in %s, and \"owngit setup-link\" makes a new one.\n", path)
	return path, nil
}

// setupBases returns the origins a setup link can use: the saved or
// running base URL, then the listen address, where listening on every
// address means this computer's LAN addresses. A computer with a screen
// lists 127.0.0.1 before them.
func setupBases(ctx context.Context, store *state.Store) ([]string, error) {
	observed, err := store.ObserveRunningNetwork(ctx)
	if err != nil {
		return nil, err
	}
	listen, base := server.DefaultListenAddress, ""
	if observed.Server == state.ServerRunning && observed.Record != nil {
		listen, base = observed.Record.Address, observed.Record.BaseURL
	} else {
		saved, err := store.NetworkSettings(ctx)
		if err != nil {
			return nil, err
		}
		listen = cmp.Or(saved.Listen, listen)
		base = saved.BaseURL
	}
	var bases []string
	if base != "" {
		bases = append(bases, base)
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, fmt.Errorf("listen address %q: %w", listen, err)
	}
	if ip, err := netip.ParseAddr(host); host != "" && (err != nil || !ip.IsUnspecified()) {
		bases = append(bases, "http://"+net.JoinHostPort(host, port))
	} else {
		if !probeEnvironment().Headless() {
			bases = append(bases, "http://"+net.JoinHostPort("127.0.0.1", port))
		}
		for _, address := range lanAddresses() {
			bases = append(bases, "http://"+net.JoinHostPort(address.String(), port))
		}
		if len(bases) == 0 {
			bases = append(bases, "http://"+net.JoinHostPort("127.0.0.1", port))
		}
	}
	return slices.Compact(bases), nil
}

// interfaceAddress is one address of a network interface.
type interfaceAddress struct {
	name    string
	address netip.Addr
}

// lanAddresses lists the addresses other devices may reach this computer
// by, the most likely first: the address of the default route, then
// private IPv4, other IPv4 (such as a tailnet), unique local IPv6 and
// global IPv6. Container and virtual bridges are left out.
func lanAddresses() []netip.Addr {
	var preferred netip.Addr
	// Connecting a UDP socket sends nothing; it only selects the source
	// address of the default route.
	if connection, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		if local, ok := connection.LocalAddr().(*net.UDPAddr); ok {
			preferred, _ = netip.AddrFromSlice(local.IP)
			preferred = preferred.Unmap()
		}
		connection.Close()
	}
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

// maxSetupAddresses bounds how many addresses a setup link is shown for.
const maxSetupAddresses = 6

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
		case address.IsPrivate():
			return 3
		default:
			return 4
		}
	}
	var kept []interfaceAddress
	for _, candidate := range candidates {
		address := candidate.address
		if !address.IsValid() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() || virtualInterface(candidate.name) {
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
