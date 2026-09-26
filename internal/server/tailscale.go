package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"owngit/internal/requestctx"
	"owngit/internal/state"
	"owngit/internal/tailscale"
)

// Sharing on the tailnet over HTTPS: the owner turns it on, and OwnGit asks
// the host's Tailscale to answer HTTPS for this computer's MagicDNS name
// with Tailscale Serve and to pass the requests to OwnGit's local port.
// Tailscale holds the certificate and encrypts the connection on this
// computer. OwnGit trusts 127.0.0.1 as the proxy, so it sees such requests as
// HTTPS, and saves the HTTPS address as its base URL and allowed name.
//
// OwnGit reads Tailscale's Serve configuration before it writes, refuses
// when something else uses the HTTPS port, reads the configuration back
// after writing, and records what it wrote (state.TailscaleServe). Turning
// sharing off removes the endpoint only while Tailscale still has exactly
// that record's endpoint, and takes back only the settings OwnGit changed.
// It never enables Funnel, and Tailscale's identity headers grant nothing.

// TailscaleHTTPSPort is the HTTPS port OwnGit asks Tailscale to answer on,
// so the address has no port: https://box.tail1234.ts.net/.
const TailscaleHTTPSPort = 443

// loopbackProxy is the address Tailscale Serve connects to OwnGit from.
const loopbackProxy = "127.0.0.1"

// tailscaleChangeTimeout bounds one change of sharing. A change runs to its
// end even when the request that asked for it is cancelled, such as by a
// closed browser tab, so that it is not left half done; each tailscale
// command keeps its own time limit.
const tailscaleChangeTimeout = 2 * time.Minute

// Tailscale reports and changes Tailscale sharing. The Settings page and
// "owngit tailscale" use it with the same rules.
type Tailscale struct {
	Store *state.Store
	// Find returns the tailscale command.
	Find func() (tailscale.Command, error)
	// Observe tells whether a server uses the state directory and what it
	// runs with: state.ObserveRunningNetwork from another process, or
	// state.OwnRunningNetwork inside the serve process.
	Observe func(context.Context) (state.RunningObservation, error)
	// Live is the network of the running server, which a change applies to
	// at once; nil outside the serve process.
	Live *LiveNetwork
	// BeforeServe, when set, runs just before turning on asks Tailscale to
	// answer HTTPS for name, which gets the address a certificate. The
	// command line shows the certificate log notice there; the Settings page
	// shows it next to the switch.
	BeforeServe func(name string)
	// changing serializes changes inside this process, and
	// Store.LockTailscaleChange between processes, so that no change reads
	// the record of what OwnGit added while another rewrites it.
	changing sync.Mutex
	// readings shares reads of Tailscale's state among reports.
	readings readingCache
}

// Endpoint states in TailscaleReport.
const (
	// While sharing is on:
	TailscaleEndpointOwnGit  = "owngit"  // Tailscale has exactly OwnGit's endpoint
	TailscaleEndpointMissing = "missing" // Tailscale has nothing on the port
	TailscaleEndpointChanged = "changed" // something else is on the port now
	// While sharing is off:
	TailscaleEndpointFree  = "free"  // the port can be used
	TailscaleEndpointTaken = "taken" // something else uses the port
	// Either way:
	TailscaleEndpointUnknown = "unknown" // the configuration could not be read
)

// What readiness waits for, in TailscaleReport.Waiting.
const (
	TailscaleWaitTailscale  = "tailscale"          // Problem says what
	TailscaleWaitUnfinished = "unfinished"         // turning on did not finish; turn on again
	TailscaleWaitName       = "name_changed"       // the computer's name changed; turn on again
	TailscaleWaitEndpoint   = "endpoint"           // Endpoint says what
	TailscaleWaitStopped    = "server_not_running" // start OwnGit
	TailscaleWaitUnknown    = "server_unknown"     // the running server cannot be confirmed
	TailscaleWaitRestart    = "restart"            // restart OwnGit
	TailscaleWaitOption     = "start_option"       // a start option keeps the old value
)

// TailscaleReport is the state of Tailscale sharing. "owngit tailscale
// status --json" prints it and the Settings page shows it.
type TailscaleReport struct {
	// Installed says whether the tailscale command was found, and Command
	// where.
	Installed bool   `json:"installed"`
	Command   string `json:"command,omitempty"`
	// MacApp is true for the Tailscale app for macOS, which does not run
	// until someone logs in after a restart.
	MacApp bool `json:"mac_app"`
	// Problem names what keeps Tailscale from serving this computer over
	// HTTPS, as a tailscale.Kind, or is empty.
	Problem       string `json:"problem,omitempty"`
	ProblemDetail string `json:"problem_detail,omitempty"`
	// Name is this computer's MagicDNS name as Tailscale reports it now.
	Name string `json:"name,omitempty"`
	// On says that sharing is on; Sharing is its record and URL its
	// address.
	On      bool                  `json:"on"`
	Sharing *state.TailscaleServe `json:"sharing,omitempty"`
	URL     string                `json:"url,omitempty"`
	// Endpoint is one of the TailscaleEndpoint values, and Found describes
	// what else is on the HTTPS port.
	Endpoint string          `json:"endpoint"`
	Found    []tailscale.Use `json:"found,omitempty"`
	// Stale lists what Tailscale keeps on the HTTPS port under a name this
	// computer had before (tailscale.Endpoint).
	Stale []tailscale.Use `json:"stale,omitempty"`
	// Server is the state of the OwnGit server (state.RunningObservation).
	Server string `json:"server"`
	// Ready is true when the HTTPS address works: Tailscale has OwnGit's
	// endpoint, and the running server accepts the name, trusts 127.0.0.1
	// as the proxy and listens where Tailscale connects. Waiting lists what
	// is missing otherwise.
	Ready   bool     `json:"ready"`
	Waiting []string `json:"waiting,omitempty"`
	// CanTurnOn says that turning sharing on is expected to work now: it
	// is off and Tailscale and its HTTPS port are ready, or it is on and
	// waits to be turned on again. The Settings page and "owngit tailscale
	// status" offer turning on only then.
	CanTurnOn bool `json:"can_turn_on"`
	// Listen is the listen address of the next start, and HomeNetwork
	// whether it reaches other devices on the home network.
	Listen      string `json:"listen"`
	HomeNetwork bool   `json:"home_network"`
	// ListenOption is the --listen option the running server was started
	// with, or empty. It decides where that server listens, so turning
	// sharing on keeps it and the home network choice has no effect.
	ListenOption string `json:"listen_option,omitempty"`
}

// Report reads the state of Tailscale sharing without changing anything.
func (sharing *Tailscale) Report(ctx context.Context) (TailscaleReport, error) {
	record, on, err := sharing.Store.TailscaleServe(ctx)
	if err != nil {
		return TailscaleReport{}, err
	}
	saved, err := sharing.Store.NetworkSettings(ctx)
	if err != nil {
		return TailscaleReport{}, err
	}
	observed, err := sharing.Observe(ctx)
	if err != nil {
		return TailscaleReport{}, err
	}
	report := TailscaleReport{On: on, Server: observed.Server, Endpoint: TailscaleEndpointUnknown}
	report.Listen = nextListen(saved)
	host, _, _ := net.SplitHostPort(report.Listen)
	report.HomeNetwork = everyInterface(host)
	if _, fromOption := targetPort(report.Listen, observed); fromOption {
		report.ListenOption = observed.Record.Listen
	}
	if on {
		report.Sharing, report.URL = &record, "https://"+record.Name+"/"
	}

	reading, err := sharing.read(ctx)
	if err != nil {
		return TailscaleReport{}, err
	}
	if reading.commandErr != nil {
		report.Problem = string(tailscale.KindOf(reading.commandErr))
	} else {
		command := reading.command
		report.Installed, report.Command, report.MacApp = true, command.Path, command.MacApp
		err := reading.statusErr
		if err == nil {
			report.Name = reading.status.Name
			err = reading.status.Usable()
		}
		if err != nil {
			report.Problem, report.ProblemDetail = problemOf(err)
		}
		if report.Problem == "" || on {
			readEndpoint(reading, &report, record, observed)
		}
	}
	if on {
		report.Waiting = waitingFor(report, record, observed)
		report.Ready = len(report.Waiting) == 0
	}
	again := slices.Contains(report.Waiting, TailscaleWaitUnfinished) || slices.Contains(report.Waiting, TailscaleWaitName)
	report.CanTurnOn = report.Installed && report.Problem == "" &&
		(!on && report.Endpoint == TailscaleEndpointFree || on && again)
	return report, nil
}

// readEndpoint fills the Endpoint fields from Tailscale's Serve
// configuration.
func readEndpoint(reading tailscaleReading, report *TailscaleReport, record state.TailscaleServe, observed state.RunningObservation) {
	config, err := reading.config, reading.configErr
	if err != nil {
		if report.Problem == "" {
			report.Problem, report.ProblemDetail = problemOf(err)
		}
		return
	}
	if report.Name != "" {
		report.Stale = config.Endpoint(report.Name, TailscaleHTTPSPort, "").Stale
	}
	if report.On {
		endpoint := config.Endpoint(record.Name, record.HTTPSPort, record.Target)
		switch {
		case endpoint.Exact:
			report.Endpoint = TailscaleEndpointOwnGit
		case endpoint.Free:
			report.Endpoint = TailscaleEndpointMissing
		default:
			report.Endpoint, report.Found = TailscaleEndpointChanged, endpoint.Found
		}
		return
	}
	port, _ := targetPort(report.Listen, observed)
	endpoint := config.Endpoint(report.Name, TailscaleHTTPSPort, tailscale.Target(port))
	report.Endpoint = TailscaleEndpointFree
	if !endpoint.Free && !endpoint.Exact {
		report.Endpoint, report.Found = TailscaleEndpointTaken, endpoint.Found
	}
}

// waitingFor lists what keeps sharing that is on from working.
func waitingFor(report TailscaleReport, record state.TailscaleServe, observed state.RunningObservation) []string {
	var waiting []string
	if report.Problem != "" {
		waiting = append(waiting, TailscaleWaitTailscale)
	}
	// Turning on again is the one thing that helps an unfinished turning
	// on or a renamed computer, so nothing else is listed then.
	if !record.Confirmed {
		return append(waiting, TailscaleWaitUnfinished)
	}
	if report.Name != "" && report.Name != record.Name {
		return append(waiting, TailscaleWaitName)
	}
	if report.Endpoint == TailscaleEndpointMissing || report.Endpoint == TailscaleEndpointChanged {
		waiting = append(waiting, TailscaleWaitEndpoint)
	}
	switch observed.Server {
	case state.ServerNotRunning:
		waiting = append(waiting, TailscaleWaitStopped)
	case state.ServerRunning:
		running := observed.Record
		accepts := slices.Contains(running.AcceptedHosts, record.Name)
		trusts := trustsLoopback(running.TrustedProxies)
		reaches := reachesTarget(running.Listen, running.Address, record.Target)
		switch {
		case accepts && trusts && reaches:
		case !reaches && running.ListenSource == NetworkSourceFlag, !trusts && running.TrustedProxiesSource == NetworkSourceFlag:
			waiting = append(waiting, TailscaleWaitOption)
		default:
			waiting = append(waiting, TailscaleWaitRestart)
		}
	default:
		waiting = append(waiting, TailscaleWaitUnknown)
	}
	return waiting
}

// TailscaleError is a refusal or failure to show the owner. Problem is a
// tailscale.Kind or one of the TailscaleProblem values.
type TailscaleError struct {
	Problem string
	// Detail is what Tailscale printed, or the address concerned.
	Detail string
	// Found is what is on the HTTPS port.
	Found []tailscale.Use
	// MacApp is true for the Tailscale app for macOS.
	MacApp bool
}

// Problems of turning sharing on or off, besides the tailscale.Kind values.
const (
	// TailscaleProblemTaken: something else uses the HTTPS port.
	TailscaleProblemTaken = "port_taken"
	// TailscaleProblemReadBack: Tailscale did not keep the endpoint.
	TailscaleProblemReadBack = "read_back"
	// TailscaleProblemChanged: the endpoint changed since OwnGit made it.
	TailscaleProblemChanged = "endpoint_changed"
	// TailscaleProblemListenOption: the running server listens, by a start
	// option, where Tailscale cannot connect.
	TailscaleProblemListenOption = "listen_option"
	// TailscaleProblemNotOn: sharing is not on.
	TailscaleProblemNotOn = "not_on"
)

func (err *TailscaleError) Error() string {
	text := "tailscale sharing: " + err.Problem
	if err.Detail != "" {
		text += ": " + err.Detail
	}
	if len(err.Found) > 0 {
		text += " (" + TailscaleUsesText(err.Found) + ")"
	}
	return text
}

// TailscaleChange is the result of turning sharing on or off.
type TailscaleChange struct {
	Record state.TailscaleServe
	// Listen is the listen address of the next start, and ListenChanged
	// whether turning sharing on changed it.
	Listen        string
	ListenChanged bool
	// ListenOption is the --listen option of the running server, which
	// kept the listen address as it is; empty otherwise.
	ListenOption string
	// Endpoint says what happened to Tailscale's endpoint: "created",
	// "kept" (it was already there) or, when turning off, "removed", "gone"
	// (it was already removed), "left" (OwnGit had not created it) or
	// "stale" (it is under the name the computer had before a rename, which
	// only that name can remove; it answers for nothing).
	Endpoint string
	// RemovedHost is the allowed Host that turning on after a rename took
	// back: the earlier name, which OwnGit had added.
	RemovedHost string
}

// On turns sharing on. homeNetwork chooses the listen address: nil keeps it
// when Tailscale can reach it and otherwise listens on this computer only,
// true also listens on the home network, and false listens on this computer
// only. Changing the listen address applies at the next start.
func (sharing *Tailscale) On(ctx context.Context, homeNetwork *bool) (TailscaleChange, error) {
	ctx, unlock, err := sharing.lock(ctx)
	if err != nil {
		return TailscaleChange{}, err
	}
	defer unlock()
	defer sharing.forget()
	change, err := sharing.on(ctx, homeNetwork)
	if err == nil && sharing.Live != nil {
		sharing.Live.ApplyTailscale(change.Record, change.RemovedHost)
	}
	return change, err
}

// lock starts a change: it runs to its end even when ctx is cancelled,
// within tailscaleChangeTimeout, and after every other change of this state
// directory.
func (sharing *Tailscale) lock(ctx context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tailscaleChangeTimeout)
	sharing.changing.Lock()
	release, err := sharing.Store.LockTailscaleChange(ctx)
	if err != nil {
		sharing.changing.Unlock()
		cancel()
		return nil, nil, err
	}
	return ctx, func() {
		release()
		sharing.changing.Unlock()
		cancel()
	}, nil
}

func (sharing *Tailscale) on(ctx context.Context, homeNetwork *bool) (TailscaleChange, error) {
	command, err := sharing.findCommand()
	if err != nil {
		return TailscaleChange{}, tailscaleError(err, false)
	}
	status, err := command.Status(ctx)
	if err == nil {
		err = status.Usable()
	}
	if err != nil {
		return TailscaleChange{}, tailscaleError(err, command.MacApp)
	}
	previous, wasOn, err := sharing.Store.TailscaleServe(ctx)
	if err != nil {
		return TailscaleChange{}, err
	}
	saved, err := sharing.Store.NetworkSettings(ctx)
	if err != nil {
		return TailscaleChange{}, err
	}
	observed, err := sharing.Observe(ctx)
	if err != nil {
		return TailscaleChange{}, err
	}
	change := TailscaleChange{Listen: nextListen(saved)}
	port, fromOption := targetPort(change.Listen, observed)
	if fromOption {
		change.ListenOption = observed.Record.Listen
		if host, _, _ := net.SplitHostPort(observed.Record.Listen); !reachableAtLoopback(host) {
			return TailscaleChange{}, &TailscaleError{Problem: TailscaleProblemListenOption, Detail: observed.Record.Listen}
		}
	} else {
		listen := planListen(change.Listen, homeNetwork)
		change.ListenChanged = listen != change.Listen
		change.Listen = listen
		_, portText, _ := net.SplitHostPort(listen)
		port, _ = strconv.Atoi(portText)
	}
	target := tailscale.Target(port)

	// Read before writing: the port must be free, or hold exactly OwnGit's
	// endpoint, whether from this record or set up the same way by hand.
	config, err := command.ServeConfig(ctx)
	if err != nil {
		return TailscaleChange{}, tailscaleError(err, command.MacApp)
	}
	endpoint := config.Endpoint(status.Name, TailscaleHTTPSPort, target)
	// After a rename OwnGit's earlier endpoint is under the old name, which
	// answers for nothing and does not take the port for the new one
	// (tailscale.Endpoint.Stale), so the new name gets its own endpoint.
	ours := wasOn && previous.Name == status.Name && config.Endpoint(previous.Name, previous.HTTPSPort, previous.Target).Exact
	// A record that was never confirmed belongs to a turning on that was
	// interrupted before it changed any setting, so this one starts anew:
	// it records the base URL saved now and what it adds itself.
	fresh := !wasOn || !previous.Confirmed
	record := previous
	if fresh {
		record = state.TailscaleServe{CreatedAt: time.Now().Unix()}
	}
	record.Name, record.HTTPSPort, record.Target = status.Name, TailscaleHTTPSPort, target
	switch {
	case endpoint.Exact:
		change.Endpoint = "kept"
		record.Created = wasOn && previous.Created
	case endpoint.Free || ours:
		change.Endpoint = "created"
		record.Created = true
	default:
		return TailscaleChange{}, &TailscaleError{Problem: TailscaleProblemTaken, Found: endpoint.Found}
	}

	if change.Endpoint == "created" {
		// A new record is saved before writing, so an endpoint that is
		// written and then interrupted can still be found and removed. An
		// existing record keeps describing the endpoint it made until the
		// new one is confirmed.
		if fresh {
			if err := sharing.Store.SaveTailscaleServe(ctx, record); err != nil {
				return TailscaleChange{}, err
			}
		}
		if sharing.BeforeServe != nil {
			sharing.BeforeServe(record.Name)
		}
		writeErr := command.ServeHTTPS(ctx, TailscaleHTTPSPort, target)
		after, readErr := command.ServeConfig(ctx)
		if writeErr == nil && readErr == nil && !after.Endpoint(record.Name, TailscaleHTTPSPort, target).Exact {
			writeErr = &TailscaleError{Problem: TailscaleProblemReadBack, MacApp: command.MacApp}
		}
		if writeErr == nil {
			writeErr = readErr
		}
		if writeErr != nil {
			if readErr == nil && fresh && after.Endpoint(record.Name, TailscaleHTTPSPort, target).Free {
				_ = sharing.Store.ClearTailscaleServe(ctx)
			}
			return TailscaleChange{}, tailscaleError(writeErr, command.MacApp)
		}
	}

	update, err := sharing.onUpdate(ctx, saved, change.Listen, &record, fresh)
	if err != nil {
		return TailscaleChange{}, err
	}
	if len(update.RemoveHosts) > 0 {
		change.RemovedHost = previous.AddedHost
	}
	// Plain HTTP is accepted only when this change opens the home network.
	if host, _, _ := net.SplitHostPort(change.Listen); change.ListenChanged && !IsLoopbackHost(host) {
		if err := sharing.Store.AcknowledgeInsecureHTTP(ctx); err != nil {
			return TailscaleChange{}, err
		}
	}
	if err := sharing.Store.UpdateNetwork(ctx, update); err != nil {
		return TailscaleChange{}, err
	}
	change.Record = record
	return change, nil
}

// onUpdate is the settings change of turning sharing on: the listen
// address, the HTTPS base URL, 127.0.0.1 as a trusted proxy and the name as
// an allowed Host, each only when not already saved, and the confirmed
// record naming what was added.
func (sharing *Tailscale) onUpdate(ctx context.Context, saved state.NetworkSettings, listen string, record *state.TailscaleServe, fresh bool) (state.NetworkUpdate, error) {
	proxies, err := sharing.Store.TrustedProxies(ctx)
	if err != nil {
		return state.NetworkUpdate{}, err
	}
	hosts, err := sharing.Store.TrustedHosts(ctx)
	if err != nil {
		return state.NetworkUpdate{}, err
	}
	update := state.NetworkUpdate{Settings: saved}
	if listen != nextListen(saved) {
		update.Settings.Listen = listen
	}
	origin := "https://" + record.Name
	if fresh {
		record.PreviousBaseURL = saved.BaseURL
	}
	update.Settings.BaseURL, record.BaseURL = origin, origin
	// After a rename, the earlier name that OwnGit added is taken back.
	if record.AddedHost != "" && record.AddedHost != record.Name {
		update.RemoveHosts = matchingHosts(hosts, record.AddedHost)
		record.AddedHost = ""
	}
	if !trustsLoopback(proxies) {
		update.AddProxies, record.AddedProxy = []string{loopbackProxy}, loopbackProxy
	}
	if !slices.Contains(NormalizedHosts(hosts), record.Name) {
		update.AddHosts, record.AddedHost = []string{record.Name}, record.Name
	}
	record.Confirmed = true
	update.Tailscale = record
	return update, nil
}

// Off turns sharing off. It removes Tailscale's endpoint only when OwnGit
// created it and Tailscale still has exactly that endpoint; when the
// endpoint changed it explains and changes nothing. Then it takes back the
// settings OwnGit changed, where they are still as OwnGit saved them. The
// listen address stays.
func (sharing *Tailscale) Off(ctx context.Context) (TailscaleChange, error) {
	ctx, unlock, err := sharing.lock(ctx)
	if err != nil {
		return TailscaleChange{}, err
	}
	defer unlock()
	defer sharing.forget()
	change, baseURL, err := sharing.off(ctx)
	if err == nil && sharing.Live != nil {
		sharing.Live.RemoveTailscale(change.Record, baseURL)
	}
	return change, err
}

// off turns sharing off and returns the base URL saved now.
func (sharing *Tailscale) off(ctx context.Context) (TailscaleChange, string, error) {
	record, on, err := sharing.Store.TailscaleServe(ctx)
	if err != nil {
		return TailscaleChange{}, "", err
	}
	if !on {
		return TailscaleChange{}, "", &TailscaleError{Problem: TailscaleProblemNotOn}
	}
	change := TailscaleChange{Record: record, Endpoint: "left"}
	if record.Created {
		command, err := sharing.findCommand()
		if err != nil {
			return TailscaleChange{}, "", tailscaleError(err, false)
		}
		status, err := command.Status(ctx)
		if err != nil {
			return TailscaleChange{}, "", tailscaleError(err, command.MacApp)
		}
		config, err := command.ServeConfig(ctx)
		if err != nil {
			return TailscaleChange{}, "", tailscaleError(err, command.MacApp)
		}
		endpoint := config.Endpoint(record.Name, record.HTTPSPort, record.Target)
		switch {
		case status.Name != "" && status.Name != record.Name:
			// "tailscale serve" removes handlers only under the current
			// name, and the endpoint under the old one answers for nothing.
			// OwnGit takes back its settings and leaves the rest to the
			// owner (Stale in the report says how).
			change.Endpoint = "gone"
			if !endpoint.Free {
				change.Endpoint = "stale"
			}
		case endpoint.Exact:
			if err := command.RemoveHTTPS(ctx, record.HTTPSPort); err != nil {
				return TailscaleChange{}, "", tailscaleError(err, command.MacApp)
			}
			after, err := command.ServeConfig(ctx)
			if err != nil {
				return TailscaleChange{}, "", tailscaleError(err, command.MacApp)
			}
			if after.Endpoint(record.Name, record.HTTPSPort, record.Target).Exact {
				return TailscaleChange{}, "", &TailscaleError{Problem: TailscaleProblemReadBack, MacApp: command.MacApp}
			}
			change.Endpoint = "removed"
		case endpoint.Free:
			change.Endpoint = "gone"
		default:
			return TailscaleChange{}, "", &TailscaleError{Problem: TailscaleProblemChanged, Found: endpoint.Found}
		}
	}

	saved, err := sharing.Store.NetworkSettings(ctx)
	if err != nil {
		return TailscaleChange{}, "", err
	}
	update := state.NetworkUpdate{Settings: saved, ClearTailscale: true}
	if saved.BaseURL == record.BaseURL {
		update.Settings.BaseURL = record.PreviousBaseURL
	}
	if record.AddedProxy != "" {
		update.RemoveProxies = []string{record.AddedProxy}
	}
	if record.AddedHost != "" {
		hosts, err := sharing.Store.TrustedHosts(ctx)
		if err != nil {
			return TailscaleChange{}, "", err
		}
		update.RemoveHosts = matchingHosts(hosts, record.AddedHost)
	}
	if err := sharing.Store.UpdateNetwork(ctx, update); err != nil {
		return TailscaleChange{}, "", err
	}
	change.Listen = nextListen(saved)
	return change, update.Settings.BaseURL, nil
}

// matchingHosts returns the saved hosts that name normalizes to.
func matchingHosts(hosts []string, name string) []string {
	var matching []string
	for _, host := range hosts {
		if normalized, err := NormalizeHost(host); err == nil && normalized == name {
			matching = append(matching, host)
		}
	}
	return matching
}

// throughTailscale reports whether a request came through this server's
// Tailscale Serve endpoint: HTTPS as forwarded by a trusted proxy at the
// loopback address, for the Tailscale name the server shares. The
// connection indicator then says that Tailscale on this computer encrypted
// it.
func (app *App) throughTailscale(request *http.Request) bool {
	if app.Network == nil {
		return false
	}
	name := app.Network.TailscaleName()
	info := requestctx.Of(request)
	if name == "" || !info.Secure() || !info.Proxied {
		return false
	}
	host, err := NormalizeHost(info.Host)
	if err != nil || host != name {
		return false
	}
	peer, _, err := net.SplitHostPort(info.Peer)
	return err == nil && peer == loopbackProxy
}

func (sharing *Tailscale) findCommand() (tailscale.Command, error) {
	if sharing.Find == nil {
		return tailscale.Command{}, tailscale.ErrNotInstalled
	}
	return sharing.Find()
}

// tailscaleError turns a failure into a TailscaleError.
func tailscaleError(err error, macApp bool) error {
	var refusal *TailscaleError
	if errors.As(err, &refusal) {
		return refusal
	}
	var failure *tailscale.Error
	if errors.As(err, &failure) {
		return &TailscaleError{Problem: string(failure.Kind), Detail: failure.Detail, MacApp: macApp}
	}
	return err
}

// problemOf names a Tailscale failure for a report.
func problemOf(err error) (string, string) {
	var failure *tailscale.Error
	if errors.As(err, &failure) {
		return string(failure.Kind), failure.Detail
	}
	return string(tailscale.KindFailed), ""
}

// nextListen is the listen address of the next start without options.
func nextListen(saved state.NetworkSettings) string {
	if saved.Listen != "" {
		return saved.Listen
	}
	return DefaultListenAddress
}

// targetPort is the port Tailscale must connect to: the running server's
// port when a start option chose its listen address, since the option wins
// at every start, and otherwise the port of listen.
func targetPort(listen string, observed state.RunningObservation) (int, bool) {
	if running := observed.Record; running != nil && running.ListenSource == NetworkSourceFlag {
		if _, port, err := net.SplitHostPort(running.Address); err == nil {
			number, _ := strconv.Atoi(port)
			return number, true
		}
	}
	_, port, _ := net.SplitHostPort(listen)
	number, _ := strconv.Atoi(port)
	return number, false
}

// planListen chooses the listen address for sharing. Tailscale Serve
// connects to 127.0.0.1, so a listen address it cannot reach is replaced;
// otherwise the owner's choice, when given, decides whether other devices on
// the home network can also connect. Nothing opens the home network unless
// homeNetwork is true.
func planListen(listen string, homeNetwork *bool) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch {
	case homeNetwork == nil && reachableAtLoopback(host):
		return listen
	case homeNetwork != nil && *homeNetwork:
		if everyInterface(host) {
			return listen
		}
		return net.JoinHostPort("0.0.0.0", port)
	case homeNetwork != nil && !*homeNetwork && reachableAtLoopback(host) && !everyInterface(host):
		return listen
	}
	return net.JoinHostPort(loopbackProxy, port)
}

// everyInterface reports whether a listen host listens on every network.
func everyInterface(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}

// reachableAtLoopback reports whether a server listening on host accepts
// connections to 127.0.0.1.
func reachableAtLoopback(host string) bool {
	return everyInterface(host) || host == loopbackProxy || strings.EqualFold(host, "localhost")
}

// reachesTarget reports whether a server with this requested and bound
// address accepts the connections Tailscale makes to target.
func reachesTarget(listen, address, target string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || !reachableAtLoopback(host) {
		return false
	}
	_, port, err := net.SplitHostPort(address)
	return err == nil && target == "http://"+net.JoinHostPort(loopbackProxy, port)
}

// trustsLoopback reports whether proxies, canonical trusted proxies, include
// 127.0.0.1.
func trustsLoopback(proxies []string) bool {
	loopback := netip.MustParseAddr(loopbackProxy)
	for _, proxy := range proxies {
		if prefix, err := requestctx.ParseTrustedProxy(proxy); err == nil && prefix.Contains(loopback) {
			return true
		}
	}
	return false
}
