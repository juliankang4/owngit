package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"owngit/internal/requestctx"
	"owngit/internal/server"
	"owngit/internal/state"
)

// Network settings: the listen address, the base URL other devices use, the
// allowed Host names and the trusted reverse proxies. serve takes each value
// from its flag, then from the saved setting, then from the default. The saved values apply at the
// next start; a flag overrides one for that run only and changes nothing
// saved. Loopback names are always accepted, so no saved value can lock the
// host itself out, and "owngit network reset" goes back to the defaults.

const (
	sourceFlag    = server.NetworkSourceFlag
	sourceSaved   = server.NetworkSourceSaved
	sourceDefault = server.NetworkSourceDefault
)

// serveNetwork is the network configuration one serve run uses.
type serveNetwork struct {
	Listen        string
	ListenSource  string
	BaseURL       string // "" derives addresses from each request
	BaseURLSource string
}

// effectiveServeNetwork applies flag over saved over default. A flag value is
// checked where it is used, as before saved settings existed; a saved value
// is checked here so a bad one names its recovery command.
func effectiveServeNetwork(saved state.NetworkSettings, flags *flag.FlagSet, listenFlag, baseURLFlag string) (serveNetwork, error) {
	set := map[string]bool{}
	flags.Visit(func(entry *flag.Flag) { set[entry.Name] = true })
	network := serveNetwork{Listen: server.DefaultListenAddress, ListenSource: sourceDefault, BaseURLSource: sourceDefault}
	switch {
	case set["listen"]:
		network.Listen, network.ListenSource = listenFlag, sourceFlag
	case saved.Listen != "":
		if err := server.ValidateListenAddress(saved.Listen); err != nil {
			return serveNetwork{}, fmt.Errorf("saved %w; run \"owngit network reset\" to go back to %s", err, server.DefaultListenAddress)
		}
		network.Listen, network.ListenSource = saved.Listen, sourceSaved
	}
	switch {
	case set["base-url"]:
		network.BaseURL, network.BaseURLSource = baseURLFlag, sourceFlag
	case saved.BaseURL != "":
		canonical, err := server.ValidateBaseURL(saved.BaseURL)
		if err != nil {
			return serveNetwork{}, fmt.Errorf("saved %w; run \"owngit network reset\" to remove it", err)
		}
		network.BaseURL, network.BaseURLSource = canonical, sourceSaved
	}
	return network, nil
}

// serveProxies are the trusted reverse proxies of one serve run. List is the
// canonical text of Prefixes, sorted.
type serveProxies struct {
	Prefixes []netip.Prefix
	List     []string
	Source   string
}

// effectiveTrustedProxies applies flag over saved over default (none) to the
// trusted proxies. A --trusted-proxy flag replaces the whole saved list for
// that run; an empty flag value trusts none.
func effectiveTrustedProxies(saved []string, flags *flag.FlagSet, flagValues []string) (serveProxies, error) {
	set := false
	flags.Visit(func(entry *flag.Flag) { set = set || entry.Name == "trusted-proxy" })
	proxies := serveProxies{List: []string{}, Source: sourceDefault}
	values := saved
	if set {
		proxies.Source, values = sourceFlag, flagValues
	} else if len(saved) > 0 {
		proxies.Source = sourceSaved
	}
	for _, value := range values {
		if set && value == "" {
			continue
		}
		prefix, err := requestctx.ParseTrustedProxy(value)
		if err != nil {
			if set {
				return serveProxies{}, fmt.Errorf("--trusted-proxy: %w", err)
			}
			return serveProxies{}, fmt.Errorf("saved %w; run \"owngit network set --remove-trusted-proxy '%s'\" or \"owngit network reset --clear-trusted-proxies\" to remove it", err, value)
		}
		if text := requestctx.FormatTrustedProxy(prefix); !slices.Contains(proxies.List, text) {
			proxies.Prefixes, proxies.List = append(proxies.Prefixes, prefix), append(proxies.List, text)
		}
	}
	slices.Sort(proxies.List)
	return proxies, nil
}

// listenError explains a listen failure; a saved address names its recovery.
func (network serveNetwork) listenError(err error) error {
	if network.ListenSource == sourceSaved {
		return fmt.Errorf("listen on %s (saved with \"owngit network set\"): %w; run \"owngit network reset\" to go back to %s", network.Listen, err, server.DefaultListenAddress)
	}
	return fmt.Errorf("listen on %s: %w", network.Listen, err)
}

// claimRunningRecord takes the running-record lock for this serve process
// (state.ClaimRunningNetwork). It returns the release to run after the
// record is cleared at shutdown, and whether the lock is held. Without the
// lock this run publishes no record, and neither "owngit network show" nor
// the Settings page can vouch for what it uses.
func claimRunningRecord(ctx context.Context, store *state.Store, logf func(string, ...any)) (func(), bool) {
	release, err := store.ClaimRunningNetwork(ctx)
	if err != nil {
		logf("could not take the running-settings lock; neither \"owngit network show\" nor the Network section of Settings will report what this server uses: %v", err)
		return func() {}, false
	}
	return release, true
}

// liveNetwork is what this serve run uses for the network, published for
// "owngit network show" and the Settings page each time it changes. The
// returned unpublish removes the record when serving stops. Without the
// running-record lock (live false) nothing is published.
func liveNetwork(store *state.Store, live bool, network serveNetwork, proxies serveProxies, address, origin string, savedHosts, flagHosts []string, policy *server.HostPolicy, logf func(string, ...any)) (*server.LiveNetwork, func(), error) {
	config := server.LiveNetworkConfig{
		Record: state.RunningNetwork{
			PID: os.Getpid(), StartedAt: time.Now().Unix(),
			Listen: network.Listen, Address: address, ListenSource: network.ListenSource,
			BaseURL: network.BaseURL, BaseURLSource: network.BaseURLSource, Origin: origin,
			SavedHosts:     server.NormalizedHosts(savedHosts),
			TrustedProxies: proxies.List, TrustedProxiesSource: proxies.Source,
		},
		BaseURL: configuredOrigin(network, origin), Proxies: proxies.Prefixes, Hosts: policy, FlagHosts: flagHosts,
	}
	record, sharing, err := store.TailscaleServe(context.Background())
	if err != nil {
		return nil, nil, err
	}
	if sharing {
		config.Tailscale = &record
	}
	unpublish := func() {}
	if live {
		config.Publish = func(running state.RunningNetwork) {
			if err := store.PublishRunningNetwork(context.Background(), running); err != nil {
				logf("could not record the running network settings for \"owngit network show\": %v", err)
			}
		}
		unpublish = func() {
			if err := store.ClearRunningNetwork(context.Background()); err != nil {
				logf("could not clear the running network settings: %v", err)
			}
		}
	}
	return server.NewLiveNetwork(config), unpublish, nil
}

func networkCommand(arguments []string) error {
	if len(arguments) == 0 {
		printNetworkUsage(os.Stderr)
		return cliProblem("invalid_arguments", "network requires show, set, or reset.")
	}
	if isHelpArgument(arguments[0]) {
		printNetworkUsage(os.Stdout)
		return nil
	}
	switch arguments[0] {
	case "show":
		return networkShow(arguments[1:])
	case "set":
		return networkSet(arguments[1:])
	case "reset":
		return networkReset(arguments[1:])
	default:
		return cliProblem("invalid_arguments", "Unknown network command: "+arguments[0])
	}
}

func printNetworkUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: owngit network <show|set|reset> [options]")
	fmt.Fprintln(writer, "  network show [--json]                 saved settings and, when OwnGit runs, the values it uses")
	fmt.Fprintln(writer, "  network set [--listen ADDR] [--base-url URL] [--allowed-host HOST ...] [--remove-allowed-host HOST ...]")
	fmt.Fprintln(writer, "              [--trusted-proxy ADDR_OR_CIDR ...] [--remove-trusted-proxy ADDR_OR_CIDR ...]")
	fmt.Fprintln(writer, "  network reset [--clear-allowed-hosts] [--clear-trusted-proxies]  remove the saved listen address and base URL")
	fmt.Fprintln(writer, "Saved settings apply at the next start. A serve flag overrides one for that run only.")
}

func newNetworkFlags(name string) (*flag.FlagSet, *string) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags, flags.String("state-dir", defaultStateDir(), "host-local state directory")
}

// networkReport is the JSON form of "owngit network show". The Settings
// page renders the same report.
type networkReport = server.NetworkReport

func networkShow(arguments []string) error {
	flags, stateDir := newNetworkFlags("network show")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("network show accepts no positional arguments")
	}
	if err := state.RequireExisting(*stateDir); err != nil {
		return err
	}
	store, err := openLiveState(context.Background(), *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := context.Background()
	saved, err := store.NetworkSettings(ctx)
	if err != nil {
		return err
	}
	hosts, err := store.TrustedHosts(ctx)
	if err != nil {
		return err
	}
	proxies, err := store.TrustedProxies(ctx)
	if err != nil {
		return err
	}
	observed, err := store.ObserveRunningNetwork(ctx)
	if err != nil {
		return err
	}
	report := server.NewNetworkReport(saved, hosts, proxies)
	report.SetServer(observed)
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	printNetworkReport(os.Stdout, report)
	return nil
}

func printNetworkReport(writer io.Writer, report networkReport) {
	fmt.Fprintln(writer, "Saved settings (used at the next start; a serve flag overrides one for that run):")
	listen := report.Saved.Listen
	if listen == "" {
		listen = "not saved, default " + server.DefaultListenAddress
	}
	baseURL := report.Saved.BaseURL
	if baseURL == "" {
		baseURL = "not saved, addresses follow the address each client uses"
	}
	fmt.Fprintf(writer, "  Listen address: %s\n  Base URL:       %s\n  Allowed Hosts:  %s\n", listen, baseURL, hostList(report.Saved.AllowedHosts))
	fmt.Fprintln(writer, "  localhost, 127.0.0.1 and ::1 are always accepted.")
	fmt.Fprintf(writer, "  Trusted proxies: %s\n", hostList(report.Saved.TrustedProxies))
	if report.StaleRecord {
		fmt.Fprintln(writer, "A record left by an OwnGit server that stopped without cleaning up was ignored.")
	}
	switch report.Server {
	case server.NetworkNotRunning:
		fmt.Fprintln(writer, "No OwnGit server is running on this state directory. The saved settings apply when it starts.")
		return
	case server.NetworkStarting:
		fmt.Fprintln(writer, "An OwnGit server is starting on this state directory. Run this command again in a moment to see the values it uses.")
		return
	case server.NetworkUnknown:
		fmt.Fprintln(writer, "Something is using this state directory, such as an older OwnGit server or an offline backup, but it did not record its network settings. Restart OwnGit to be sure the saved settings apply.")
		return
	}
	running := report.Running
	fmt.Fprintf(writer, "Running server (process %d, started %s):\n", running.PID, time.Unix(running.StartedAt, 0).Format(time.RFC3339))
	fmt.Fprintf(writer, "  Listen address: %s (%s), listening on %s\n", running.Listen, running.ListenSource, running.Address)
	if running.BaseURL == "" {
		fmt.Fprintf(writer, "  Base URL:       none, addresses follow the address each client uses (%s)\n", running.BaseURLSource)
	} else {
		fmt.Fprintf(writer, "  Base URL:       %s (%s)\n", running.BaseURL, running.BaseURLSource)
	}
	fmt.Fprintf(writer, "  Accepted Hosts: %s\n", hostList(running.AcceptedHosts))
	if running.TrustedProxiesSource != "" {
		fmt.Fprintf(writer, "  Trusted proxies: %s (%s)\n", hostList(running.TrustedProxies), running.TrustedProxiesSource)
	}
	flagged := running.ListenSource == sourceFlag || running.BaseURLSource == sourceFlag || running.TrustedProxiesSource == sourceFlag
	if flagged {
		fmt.Fprintln(writer, "The running server was started with --listen, --base-url or --trusted-proxy, which overrides the saved value for that run. If a service starts OwnGit with that option, remove it there for the saved value to apply.")
	}
	switch {
	case report.RestartNeeded:
		fmt.Fprintln(writer, "Restart OwnGit to apply the saved settings.")
	case flagged:
		fmt.Fprintln(writer, "Apart from the values given as options, the running server uses the saved settings.")
	default:
		fmt.Fprintln(writer, "The running server uses the saved settings.")
	}
}

func networkSet(arguments []string) error {
	flags, stateDir := newNetworkFlags("network set")
	listen := flags.String("listen", "", "listen address as host:port; an empty value removes the saved one")
	baseURL := flags.String("base-url", "", "the address other devices use, an http or https origin such as http://gitbox.internal:7654; an empty value removes the saved one")
	var addHosts, removeHosts, addProxies, removeProxies stringList
	flags.Var(&addHosts, "allowed-host", "allow this Host `name` (repeatable)")
	flags.Var(&removeHosts, "remove-allowed-host", "stop allowing this Host `name` (repeatable)")
	flags.Var(&addProxies, "trusted-proxy", "trust forwarded headers from this reverse proxy `address` or CIDR range (repeatable)")
	flags.Var(&removeProxies, "remove-trusted-proxy", "stop trusting this proxy `address` or CIDR range (repeatable)")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("network set accepts no positional arguments")
	}
	set := map[string]bool{}
	flags.Visit(func(entry *flag.Flag) { set[entry.Name] = true })
	if !set["listen"] && !set["base-url"] && len(addHosts) == 0 && len(removeHosts) == 0 && len(addProxies) == 0 && len(removeProxies) == 0 {
		printNetworkUsage(os.Stderr)
		return errors.New("network set needs --listen, --base-url, --allowed-host, --remove-allowed-host, --trusted-proxy, or --remove-trusted-proxy")
	}
	// Every value is checked before anything is saved.
	if set["listen"] && *listen != "" {
		if err := server.ValidateListenAddress(*listen); err != nil {
			return err
		}
	}
	if set["base-url"] && *baseURL != "" {
		canonical, err := server.ValidateBaseURL(*baseURL)
		if err != nil {
			return err
		}
		*baseURL = canonical
	}
	add, err := normalizeHostArguments(addHosts)
	if err != nil {
		return err
	}
	remove, err := normalizeHostArguments(removeHosts)
	if err != nil {
		return err
	}
	for _, host := range add {
		if slices.Contains(remove, host) {
			return fmt.Errorf("%s is both allowed and removed", host)
		}
	}
	trust, err := canonicalProxies(addProxies)
	if err != nil {
		return err
	}
	distrust := removableProxies(removeProxies)
	for _, proxy := range trust {
		if slices.Contains(distrust, proxy) {
			return fmt.Errorf("%s is both trusted and removed", proxy)
		}
	}
	ctx := context.Background()
	store, err := openLiveState(ctx, *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	update := state.NetworkUpdate{AddHosts: add, AddProxies: trust, RemoveProxies: distrust}
	if update.Settings, err = store.NetworkSettings(ctx); err != nil {
		return err
	}
	savedProxies, err := store.TrustedProxies(ctx)
	if err != nil {
		return err
	}
	for _, proxy := range distrust {
		if !slices.Contains(savedProxies, proxy) {
			fmt.Printf("%s was not a trusted proxy.\n", proxy)
		}
	}
	if set["listen"] {
		update.Settings.Listen = *listen
	}
	if set["base-url"] {
		update.Settings.BaseURL = *baseURL
	}
	stored, err := store.TrustedHosts(ctx)
	if err != nil {
		return err
	}
	// Stored names may predate normalization, so removal compares the
	// normalized form and removes the stored spelling.
	for _, host := range remove {
		found := false
		for _, value := range stored {
			if normalized, err := server.NormalizeHost(value); err == nil && normalized == host {
				update.RemoveHosts, found = append(update.RemoveHosts, value), true
			}
		}
		if !found {
			fmt.Printf("%s was not an allowed Host.\n", host)
		}
	}
	if err := store.UpdateNetwork(ctx, update); err != nil {
		return err
	}
	fmt.Println("Network settings saved. They apply at the next start of OwnGit; a serve flag still overrides one for that run.")
	if set["listen"] && *listen != "" {
		host, _, _ := net.SplitHostPort(*listen)
		if !server.IsLoopbackHost(host) {
			fmt.Println("Note: other devices will reach OwnGit over plain HTTP, which is not encrypted. A reverse proxy with HTTPS or Tailscale HTTPS gives an encrypted connection.")
		}
		// Listening on every interface accepts no new name by itself, so
		// other devices would be refused until a name is allowed.
		allowed, err := store.TrustedHosts(ctx)
		if err != nil {
			return err
		}
		if (host == "" || net.ParseIP(host).IsUnspecified()) && update.Settings.BaseURL == "" && len(allowed) == 0 {
			fmt.Println("Note: other devices must use a name OwnGit accepts. Save it with --base-url or --allowed-host.")
		}
	}
	proxies, err := store.TrustedProxies(ctx)
	if err != nil {
		return err
	}
	if strings.HasPrefix(update.Settings.BaseURL, "https:") && len(proxies) == 0 {
		fmt.Println("Note: the base URL uses https but no reverse proxy is trusted. Name the proxy with --trusted-proxy so that OwnGit treats requests through it as HTTPS.")
	}
	return nil
}

// removableProxies returns the stored text to remove for each argument: the
// canonical form of a valid proxy, or the value as given otherwise, so a
// saved value that no longer validates can still be removed.
func removableProxies(values []string) []string {
	var proxies []string
	for _, value := range values {
		text := strings.TrimSpace(value)
		if prefix, err := requestctx.ParseTrustedProxy(value); err == nil {
			text = requestctx.FormatTrustedProxy(prefix)
		}
		if !slices.Contains(proxies, text) {
			proxies = append(proxies, text)
		}
	}
	return proxies
}

// canonicalProxies parses trusted proxy arguments into their canonical text.
func canonicalProxies(values []string) ([]string, error) {
	var proxies []string
	for _, value := range values {
		prefix, err := requestctx.ParseTrustedProxy(value)
		if err != nil {
			return nil, err
		}
		if text := requestctx.FormatTrustedProxy(prefix); !slices.Contains(proxies, text) {
			proxies = append(proxies, text)
		}
	}
	return proxies, nil
}

func networkReset(arguments []string) error {
	flags, stateDir := newNetworkFlags("network reset")
	clearHosts := flags.Bool("clear-allowed-hosts", false, "also remove every allowed Host name")
	clearProxies := flags.Bool("clear-trusted-proxies", false, "also remove every trusted proxy")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("network reset accepts no positional arguments")
	}
	if err := state.RequireExisting(*stateDir); err != nil {
		return err
	}
	ctx := context.Background()
	store, err := openLiveState(ctx, *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	hosts, err := store.TrustedHosts(ctx)
	if err != nil {
		return err
	}
	proxies, err := store.TrustedProxies(ctx)
	if err != nil {
		return err
	}
	update := state.NetworkUpdate{}
	if *clearHosts {
		update.RemoveHosts, hosts = hosts, nil
	}
	if *clearProxies {
		update.RemoveProxies, proxies = proxies, nil
	}
	if err := store.UpdateNetwork(ctx, update); err != nil {
		return err
	}
	fmt.Printf("Saved listen address and base URL removed. At the next start OwnGit listens on %s, unless a serve flag says otherwise.\n", server.DefaultListenAddress)
	if *clearHosts {
		fmt.Println("Allowed Hosts removed. localhost, 127.0.0.1 and ::1 are always accepted.")
	} else {
		fmt.Printf("Allowed Hosts kept: %s. localhost, 127.0.0.1 and ::1 are always accepted.\n", hostList(server.NormalizedHosts(hosts)))
	}
	switch {
	case *clearProxies:
		fmt.Println("Trusted proxies removed. Forwarded headers are ignored from every address.")
	case len(proxies) > 0:
		fmt.Printf("Trusted proxies kept: %s. Remove them with --clear-trusted-proxies, or one with \"owngit network set --remove-trusted-proxy\".\n", hostList(proxies))
	}
	fmt.Println("Restart OwnGit to apply.")
	return nil
}

func normalizeHostArguments(values []string) ([]string, error) {
	var hosts []string
	for _, value := range values {
		host, err := server.NormalizeHost(value)
		if err != nil {
			return nil, fmt.Errorf("invalid Host name %q: %w", value, err)
		}
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	return hosts, nil
}

func hostList(hosts []string) string {
	if len(hosts) == 0 {
		return "none"
	}
	return strings.Join(hosts, ", ")
}

// configuredOrigin is the base URL shown in clone addresses: the canonical
// configured origin, or "" when the run has no base URL and addresses follow
// each request.
func configuredOrigin(network serveNetwork, origin string) string {
	if network.BaseURL == "" {
		return ""
	}
	return origin
}
