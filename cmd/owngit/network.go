package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"owngit/internal/server"
	"owngit/internal/state"
)

// Network settings: the listen address, the base URL other devices use, and
// the allowed Host names. serve takes each value from its flag, then from
// the saved setting, then from the default. The saved values apply at the
// next start; a flag overrides one for that run only and changes nothing
// saved. Loopback names are always accepted, so no saved value can lock the
// host itself out, and "owngit network reset" goes back to the defaults.

const (
	sourceFlag    = "flag"
	sourceSaved   = "saved"
	sourceDefault = "default"
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

// listenError explains a listen failure; a saved address names its recovery.
func (network serveNetwork) listenError(err error) error {
	if network.ListenSource == sourceSaved {
		return fmt.Errorf("listen on %s (saved with \"owngit network set\"): %w; run \"owngit network reset\" to go back to %s", network.Listen, err, server.DefaultListenAddress)
	}
	return fmt.Errorf("listen on %s: %w", network.Listen, err)
}

// The running record (state.RunningNetwork) tells saved from running values,
// which later features such as Tailscale setup rely on. It is trusted only
// while its publisher is alive: a serve process holds runningLockName in the
// state directory for its whole life, clears any record left by a crashed
// run as soon as it holds that lock, and publishes its own record once its
// listener is bound. The operating system releases the lock when the process
// ends in any way, including kill -9, so "lock held and record present" means
// the record belongs to the live holder. Other holders of the offline lock,
// such as an OwnGit 1.0.3 server or an offline backup, never take this lock,
// so a record they find is reported as stale, never as running. Unlike a
// process ID check this cannot be fooled by a reused process ID.
const runningLockName = ".network-running.lock"

// lockRetry bounds how long serve waits for a state lock that a momentary
// "owngit network show" probe holds. A real second owner holds it longer and
// still stops the start.
const lockRetry = 500 * time.Millisecond

// acquireLockBriefly takes a lock, retrying for lockRetry while another
// process holds it.
func acquireLockBriefly(acquire func() (func(), error)) (func(), error) {
	deadline := time.Now().Add(lockRetry)
	for {
		release, err := acquire()
		if !errors.Is(err, state.ErrInstanceRunning) || time.Now().After(deadline) {
			return release, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// claimRunningRecord takes the running-record lock for this serve process
// and removes a record left by a run that ended without cleanup. It returns
// the release to run after the record is cleared at shutdown. Failing to take
// the lock only means "owngit network show" cannot vouch for this run.
func claimRunningRecord(ctx context.Context, stateDir string, store *state.Store, logf func(string, ...any)) func() {
	release, err := acquireLockBriefly(func() (func(), error) {
		return state.AcquireExclusiveFileLock(filepath.Join(stateDir, runningLockName))
	})
	if err != nil {
		logf("could not take the running-settings lock; \"owngit network show\" will not report this server: %v", err)
		return func() {}
	}
	if err := store.ClearRunningNetwork(ctx); err != nil {
		logf("could not clear an earlier running network record: %v", err)
	}
	return release
}

// runningNetworkRecord publishes what this serve run uses for "owngit network
// show". publish records it, each time with the Host names the policy accepts
// then; unpublish removes it when serving stops.
func runningNetworkRecord(store *state.Store, network serveNetwork, address, origin string, savedHosts []string, policy *server.HostPolicy, logf func(string, ...any)) (publish, unpublish func()) {
	running := state.RunningNetwork{
		PID: os.Getpid(), StartedAt: time.Now().Unix(),
		Listen: network.Listen, Address: address, ListenSource: network.ListenSource,
		BaseURL: network.BaseURL, BaseURLSource: network.BaseURLSource, Origin: origin,
		SavedHosts: normalizedHosts(savedHosts),
	}
	publish = func() {
		current := running
		current.AcceptedHosts = policy.Hosts()
		if err := store.PublishRunningNetwork(context.Background(), current); err != nil {
			logf("could not record the running network settings for \"owngit network show\": %v", err)
		}
	}
	unpublish = func() {
		if err := store.ClearRunningNetwork(context.Background()); err != nil {
			logf("could not clear the running network settings: %v", err)
		}
	}
	return publish, unpublish
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
	fmt.Fprintln(writer, "  network reset [--clear-allowed-hosts]  remove the saved listen address and base URL")
	fmt.Fprintln(writer, "Saved settings apply at the next start. A serve flag overrides one for that run only.")
}

func newNetworkFlags(name string) (*flag.FlagSet, *string) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags, flags.String("state-dir", defaultStateDir(), "host-local state directory")
}

// networkReport is the JSON form of "owngit network show".
type networkReport struct {
	Saved struct {
		Listen       string   `json:"listen"`
		BaseURL      string   `json:"base_url"`
		AllowedHosts []string `json:"allowed_hosts"`
	} `json:"saved"`
	// NextStart is what a start without network flags uses.
	NextStart struct {
		Listen        string `json:"listen"`
		ListenSource  string `json:"listen_source"`
		BaseURL       string `json:"base_url"`
		BaseURLSource string `json:"base_url_source"`
	} `json:"next_start"`
	// Server is "running" when a live serve process published what it uses,
	// "starting" when that process has not published yet, "not_running", or
	// "unknown" when something else holds the state directory, such as an
	// older OwnGit or an offline backup.
	Server        string                `json:"server"`
	Running       *state.RunningNetwork `json:"running"`
	RestartNeeded bool                  `json:"restart_needed"`
	// StaleRecord says that a record left by a server that ended without
	// cleanup was found and ignored.
	StaleRecord bool `json:"stale_record"`
}

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
	// The running-record lock is probed first, so a running server's offline
	// lock is never touched. See runningLockName.
	live, err := lockHeld(func() (func(), error) {
		return state.AcquireExclusiveFileLock(filepath.Join(*stateDir, runningLockName))
	})
	if err != nil {
		return err
	}
	running, published, err := store.RunningNetwork(ctx)
	if err != nil {
		return err
	}
	held := live
	if !live {
		if held, err = lockHeld(func() (func(), error) { return state.AcquireOfflineLock(*stateDir) }); err != nil {
			return err
		}
	}
	var report networkReport
	report.Saved.Listen, report.Saved.BaseURL = saved.Listen, saved.BaseURL
	report.Saved.AllowedHosts = normalizedHosts(hosts)
	report.NextStart.Listen, report.NextStart.ListenSource = server.DefaultListenAddress, sourceDefault
	if saved.Listen != "" {
		report.NextStart.Listen, report.NextStart.ListenSource = saved.Listen, sourceSaved
	}
	report.NextStart.BaseURL, report.NextStart.BaseURLSource = saved.BaseURL, sourceDefault
	if saved.BaseURL != "" {
		report.NextStart.BaseURLSource = sourceSaved
	}
	switch {
	case live && published:
		report.Server, report.Running = "running", &running
		report.RestartNeeded = restartNeeded(report, running)
	case live:
		report.Server = "starting"
	case held:
		report.Server, report.StaleRecord = "unknown", published
	default:
		report.Server, report.StaleRecord = "not_running", published
	}
	if *asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	printNetworkReport(os.Stdout, report)
	return nil
}

// lockHeld reports whether another process holds the lock that acquire
// takes. The probe holds the lock only for an instant; serve waits for such a
// probe (acquireLockBriefly) instead of failing.
func lockHeld(acquire func() (func(), error)) (bool, error) {
	release, err := acquire()
	if errors.Is(err, state.ErrInstanceRunning) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	release()
	return false, nil
}

// restartNeeded reports whether saved settings differ from what the running
// server uses. A value the server took from a flag is not compared: the flag
// wins for that run whatever is saved. A saved Host name needs a restart when
// the running server does not accept it yet, and a removed one when the
// server loaded it at start.
func restartNeeded(report networkReport, running state.RunningNetwork) bool {
	if running.ListenSource != sourceFlag && report.NextStart.Listen != running.Listen {
		return true
	}
	if running.BaseURLSource != sourceFlag && report.NextStart.BaseURL != running.BaseURL {
		return true
	}
	for _, host := range report.Saved.AllowedHosts {
		if !slices.Contains(running.AcceptedHosts, host) {
			return true
		}
	}
	for _, host := range running.SavedHosts {
		if !slices.Contains(report.Saved.AllowedHosts, host) {
			return true
		}
	}
	return false
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
	if report.StaleRecord {
		fmt.Fprintln(writer, "A record left by an OwnGit server that stopped without cleaning up was ignored.")
	}
	switch report.Server {
	case "not_running":
		fmt.Fprintln(writer, "No OwnGit server is running on this state directory. The saved settings apply when it starts.")
		return
	case "starting":
		fmt.Fprintln(writer, "An OwnGit server is starting on this state directory. Run this command again in a moment to see the values it uses.")
		return
	case "unknown":
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
	flagged := running.ListenSource == sourceFlag || running.BaseURLSource == sourceFlag
	if flagged {
		fmt.Fprintln(writer, "The running server was started with --listen or --base-url, which overrides the saved value for that run. If a service starts OwnGit with that option, remove it there for the saved value to apply.")
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
	var addHosts, removeHosts stringList
	flags.Var(&addHosts, "allowed-host", "allow this Host `name` (repeatable)")
	flags.Var(&removeHosts, "remove-allowed-host", "stop allowing this Host `name` (repeatable)")
	if err := parseFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("network set accepts no positional arguments")
	}
	set := map[string]bool{}
	flags.Visit(func(entry *flag.Flag) { set[entry.Name] = true })
	if !set["listen"] && !set["base-url"] && len(addHosts) == 0 && len(removeHosts) == 0 {
		printNetworkUsage(os.Stderr)
		return errors.New("network set needs --listen, --base-url, --allowed-host, or --remove-allowed-host")
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
	ctx := context.Background()
	store, err := openLiveState(ctx, *stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	update := state.NetworkUpdate{AddHosts: add}
	if update.Settings, err = store.NetworkSettings(ctx); err != nil {
		return err
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
	return nil
}

func networkReset(arguments []string) error {
	flags, stateDir := newNetworkFlags("network reset")
	clearHosts := flags.Bool("clear-allowed-hosts", false, "also remove every allowed Host name")
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
	update := state.NetworkUpdate{}
	if *clearHosts {
		update.RemoveHosts, hosts = hosts, nil
	}
	if err := store.UpdateNetwork(ctx, update); err != nil {
		return err
	}
	fmt.Printf("Saved listen address and base URL removed. At the next start OwnGit listens on %s, unless a serve flag says otherwise.\n", server.DefaultListenAddress)
	if *clearHosts {
		fmt.Println("Allowed Hosts removed. localhost, 127.0.0.1 and ::1 are always accepted.")
	} else {
		fmt.Printf("Allowed Hosts kept: %s. localhost, 127.0.0.1 and ::1 are always accepted.\n", hostList(normalizedHosts(hosts)))
	}
	fmt.Println("Restart OwnGit to apply.")
	return nil
}

// openLiveState opens a state directory that a running server may be writing
// to. Opening refuses a directory that changed while it was inspected and
// says the operation can be retried, so the network commands retry a few
// times instead of failing because the server wrote at that moment.
func openLiveState(ctx context.Context, stateDir string) (*state.Store, error) {
	for attempt := 1; ; attempt++ {
		store, err := openState(ctx, stateDir, stderrf)
		if !errors.Is(err, state.ErrInspectionUnstable) || attempt == 5 {
			return store, err
		}
		time.Sleep(100 * time.Millisecond)
	}
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

// normalizedHosts returns stored Host names in the form the Host check uses,
// sorted and without duplicates. A name that cannot be normalized is kept as
// stored, so it stays visible.
func normalizedHosts(values []string) []string {
	hosts := []string{}
	for _, value := range values {
		host, err := server.NormalizeHost(value)
		if err != nil {
			host = value
		}
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	slices.Sort(hosts)
	return hosts
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
