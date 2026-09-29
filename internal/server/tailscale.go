package server

import (
	"context"
	"errors"
	"fmt"
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
// when something else uses the HTTPS port, and makes its change from what
// it read: Tailscale applies the change only while its configuration is
// still the one read, so a change made meanwhile by anything else is
// reported (TailscaleProblemServeChanged), never overwritten. OwnGit reads
// the configuration back after writing, and records what it wrote
// (state.TailscaleServe). Turning sharing off removes the endpoint only
// while Tailscale still has exactly that record's endpoint, and takes back
// only the settings OwnGit changed.
// It never enables Funnel, and Tailscale's identity headers grant nothing.

// tailscaleHTTPSPorts are the HTTPS ports that turning sharing on tries, in
// this order, when the owner names none. Port 443 gives an address without
// a port, such as https://box.tail1234.ts.net/. 8443 and 10000 are the other
// ports Tailscale names for HTTPS (the ports Funnel may use), so an address
// such as https://box.tail1234.ts.net:8443/ still reads as a web address.
// Turning on uses the first one that is free and leaves the others as they
// are; "owngit tailscale on --https-port" names any other port.
var tailscaleHTTPSPorts = []int{443, 8443, 10000}

// ErrTailscaleAhead marks a failure after Tailscale accepted a change of
// its endpoint, or showed the change, while OwnGit's settings were not
// saved: Tailscale may already have what OwnGit's settings do not.
var ErrTailscaleAhead = errors.New("OwnGit's settings were not saved, and Tailscale may already have the change")

// tailscaleMayHave reports whether Tailscale may have a change of its
// endpoint after the change command returned changeErr and the read back
// returned readErr: the read back shows the change, or it could not be read
// while the command succeeded or failed without a refusal (Refused), such as
// a timeout, so its outcome is unknown. A read back that shows no change
// rules it out. So does a change Tailscale refused (Refused), such as one
// made from a configuration that changed since OwnGit read it or one this
// user may not make, whatever the read back shows: Tailscale checks before
// it applies anything, so what the read back shows is someone else's.
func tailscaleMayHave(changeErr, readErr error, shown bool) bool {
	if changeErr != nil && refused(changeErr) {
		return false
	}
	return shown || readErr != nil
}

// refused reports whether err is a refusal the owner can fix
// (TailscaleError.Refused).
func refused(err error) bool {
	var refusal *TailscaleError
	return errors.As(tailscaleError(err, false), &refusal) && refusal.Refused()
}

// tailscaleAhead marks err with ErrTailscaleAhead when Tailscale may
// already have the change.
func tailscaleAhead(err error, changed bool) error {
	if changed {
		return fmt.Errorf("%w: %w", ErrTailscaleAhead, err)
	}
	return err
}

// TailscaleOrigin is the HTTPS origin of name on port, without the port
// when it is 443.
func TailscaleOrigin(name string, port int) string {
	if port == 443 {
		return "https://" + name
	}
	return "https://" + net.JoinHostPort(name, strconv.Itoa(port))
}

// loopbackProxy is the address Tailscale Serve connects to OwnGit from.
const loopbackProxy = "127.0.0.1"

// tailscaleChangeTimeout bounds one change of sharing. A change runs to its
// end even when the request that asked for it is cancelled, such as by a
// closed browser tab, so that it is not left half done, but it ends
// tailscaleAnswerReserve before the request's own deadline, so that the
// page still reads what it shows and answers; each call to Tailscale keeps
// its own time limit.
const tailscaleChangeTimeout = 2 * time.Minute

// tailscaleAnswerReserve is what a change leaves of its request's time for
// the page that answers it, at most half of what is left when it starts.
const tailscaleAnswerReserve = 2 * time.Second

// Tailscale reports and changes Tailscale sharing. The Settings page and
// "owngit tailscale" use it with the same rules.
type Tailscale struct {
	// Store is OwnGit's state, or nil before it exists; Report then shows
	// the defaults.
	Store *state.Store
	// Find returns the tailscale command, or an error such as
	// tailscale.ErrNotInstalled that says why there is none.
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
	// OwnGit's exact endpoint is there, but OwnGit has no record of making
	// it, such as after an interrupted change or a rename back to an
	// earlier name. It is treated as taken.
	TailscaleEndpointUnrecorded = "unrecorded"
	// Either way:
	TailscaleEndpointUnknown = "unknown" // the configuration could not be read
)

// What readiness waits for, in TailscaleReport.Waiting.
const (
	TailscaleWaitTailscale  = "tailscale"          // Problem says what
	TailscaleWaitUnfinished = "unfinished"         // turning on did not finish; turn on again
	TailscaleWaitName       = "name_changed"       // the computer's name changed; turn on again
	TailscaleWaitEndpoint   = "endpoint"           // the endpoint is gone; turn on again or off
	TailscaleWaitChanged    = "endpoint_changed"   // the endpoint changed; undo that first
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
	// what else is on the HTTPS port: the recorded port while sharing is
	// on, or the ports that turning on tries while it is off.
	Endpoint string          `json:"endpoint"`
	Found    []tailscale.Use `json:"found,omitempty"`
	// TurnOnPort is the HTTPS port that turning sharing on would use now,
	// when CanTurnOn, or the port of the unrecorded endpoint that keeps it
	// from turning on. TurnOnPassed are the ports before it that something
	// else uses.
	TurnOnPort   int   `json:"turn_on_port,omitempty"`
	TurnOnPassed []int `json:"turn_on_passed,omitempty"`
	// PortsTaken says that something else uses every HTTPS port that
	// turning on would try (listed in Found), so it cannot be turned on,
	// or on again, until one is free or another port is named.
	PortsTaken bool `json:"ports_taken,omitempty"`
	// Stale lists what Tailscale keeps on the HTTPS ports OwnGit uses under a
	// name this computer had before (tailscale.Endpoint).
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
	// status" offer turning on only then. CanTurnOff says the same of
	// turning off: it is on, and Tailscale's endpoint was not changed into
	// something turning off refuses to remove.
	CanTurnOn  bool `json:"can_turn_on"`
	CanTurnOff bool `json:"can_turn_off"`
	// Listen is the listen address of the next start, and HomeNetwork
	// whether it reaches other devices on the home network.
	Listen      string `json:"listen"`
	HomeNetwork bool   `json:"home_network"`
	// ListenOption is the --listen option the running server was started
	// with, or empty. It decides where that server listens, so turning
	// sharing on keeps it and the home network choice has no effect.
	ListenOption string `json:"listen_option,omitempty"`
	// BaseURLOption is the --base-url option the running server was
	// started with, while sharing is on and it differs from the HTTPS
	// address: that server gives out this address in clone addresses and
	// links instead.
	BaseURLOption string `json:"base_url_option,omitempty"`
}

// Report reads the state of Tailscale sharing without changing anything.
// Without a Store, before OwnGit's state exists, it reports the defaults:
// sharing off, nothing saved and no server running.
func (sharing *Tailscale) Report(ctx context.Context) (TailscaleReport, error) {
	var (
		record   state.TailscaleServe
		on       bool
		saved    state.NetworkSettings
		observed = state.RunningObservation{Server: state.ServerNotRunning}
		err      error
	)
	if sharing.Store != nil {
		if record, on, err = sharing.Store.TailscaleServe(ctx); err != nil {
			return TailscaleReport{}, err
		}
		if saved, err = sharing.Store.NetworkSettings(ctx); err != nil {
			return TailscaleReport{}, err
		}
		if observed, err = sharing.Observe(ctx); err != nil {
			return TailscaleReport{}, err
		}
	}
	report := TailscaleReport{On: on, Server: observed.Server, Endpoint: TailscaleEndpointUnknown}
	report.Listen = nextListen(saved)
	host, _, _ := net.SplitHostPort(report.Listen)
	report.HomeNetwork = everyInterface(host)
	port, fromOption := targetPort(report.Listen, observed)
	if fromOption {
		report.ListenOption = observed.Record.Listen
	}
	if on {
		report.Sharing, report.URL = &record, TailscaleOrigin(record.Name, record.HTTPSPort)+"/"
		if running := observed.Record; running != nil && running.BaseURLSource == NetworkSourceFlag && running.BaseURL != record.BaseURL {
			report.BaseURLOption = running.BaseURL
		}
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
			readEndpoint(reading, &report, record)
		}
	}
	if on {
		report.Waiting = waitingFor(report, record, observed)
		report.Ready = len(report.Waiting) == 0
	}
	again := slices.ContainsFunc(report.Waiting, func(wait string) bool {
		return wait == TailscaleWaitUnfinished || wait == TailscaleWaitName || wait == TailscaleWaitEndpoint
	})
	// What turning on would do is what it will read: the same ports in the
	// same order, for the name Tailscale reports now.
	if report.Installed && report.Problem == "" && reading.configErr == nil && (!on || again) {
		plan := planEndpoint(reading.config, report.Name, tailscale.Target(port), httpsPorts(0, record, on, report.Name), record, on)
		report.TurnOnPort, report.TurnOnPassed = plan.port, plan.passed
		switch plan.outcome {
		case endpointCreated, endpointKept:
			report.CanTurnOn = true
			if !on {
				report.Endpoint = TailscaleEndpointFree
			}
		case TailscaleProblemUnrecorded:
			if !on {
				report.Endpoint, report.Found = TailscaleEndpointUnrecorded, plan.found
			} else {
				report.PortsTaken, report.Found = true, plan.found
			}
		default:
			report.PortsTaken, report.Found = true, plan.found
			if !on {
				report.Endpoint = TailscaleEndpointTaken
			}
		}
	}
	report.CanTurnOff = on && report.Endpoint != TailscaleEndpointChanged && !offNeedsTailscale(report, record)
	return report, nil
}

// offNeedsTailscale reports whether turning off would have to change
// Tailscale's Serve configuration while a problem with Tailscale keeps it
// from doing so: turning off would be refused, so it is not offered until
// the problem is fixed. Without the endpoint, or after a rename, turning
// off changes only OwnGit's settings. Tailscale changes its Serve
// configuration only while it has this computer's node (its network map),
// which it lacks when stopped, signed out or starting; the tailscale
// command cannot help when it is missing or does not answer. Other
// problems, such as MagicDNS or HTTPS certificates being off, do not show
// that removing the endpoint fails, so turning off stays offered.
func offNeedsTailscale(report TailscaleReport, record state.TailscaleServe) bool {
	switch {
	case !record.Created, report.Endpoint == TailscaleEndpointMissing, report.Name != "" && report.Name != record.Name:
		return false
	}
	switch tailscale.Kind(report.Problem) {
	case "", tailscale.KindHTTPSOff, tailscale.KindHTTPSUnavailable, tailscale.KindMagicDNSOff, tailscale.KindNeedsApproval:
		return false
	}
	return true
}

// webPorts lists the ports that have web handlers in config, in order.
func webPorts(config tailscale.ServeConfig) []int {
	var ports []int
	for key := range config.Web {
		if _, text, err := net.SplitHostPort(key); err == nil {
			if port, err := strconv.Atoi(text); err == nil && !slices.Contains(ports, port) {
				ports = append(ports, port)
			}
		}
	}
	slices.Sort(ports)
	return ports
}

// readEndpoint fills the Endpoint fields of sharing that is on, and Stale,
// from Tailscale's Serve configuration.
func readEndpoint(reading tailscaleReading, report *TailscaleReport, record state.TailscaleServe) {
	config, err := reading.config, reading.configErr
	if err != nil {
		if report.Problem == "" {
			report.Problem, report.ProblemDetail = problemOf(err)
		}
		return
	}
	// An address under an earlier name can be on any port, also one that
	// was named and is no longer recorded, so every port with web handlers
	// is read.
	if report.Name != "" {
		for _, port := range webPorts(config) {
			report.Stale = append(report.Stale, config.Endpoint(report.Name, port, "").Stale...)
		}
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
	}
}

// Outcomes of an endpointPlan besides the refusals TailscaleProblemTaken,
// TailscaleProblemUnrecorded and TailscaleProblemOwnersEndpoint.
const (
	endpointCreated = "created" // OwnGit writes its endpoint on the port
	endpointKept    = "kept"    // the recorded port already has it
)

// endpointPlan is what turning on does with Tailscale's HTTPS ports.
type endpointPlan struct {
	// port is the port it uses, and outcome what it does there.
	port    int
	outcome string
	// passed lists the ports before it that something else uses, and found
	// what is on them, or on the port of an unrecorded endpoint.
	passed []int
	found  []tailscale.Use
	// replaces is set when the endpoint created replaces the one OwnGit
	// created there to an earlier target.
	replaces bool
}

// planEndpoint chooses the first of ports that turning on can use for name
// and target. A port is usable when it is free, or holds exactly the
// endpoint that the record previous describes; ports that something else
// uses, Funnel included, are passed over and left alone. An endpoint that
// points at OwnGit exactly, on a port without a record of OwnGit making it,
// stops the search: nothing shows that OwnGit made it, and passing it over
// would leave it pointing at OwnGit beside a second one.
func planEndpoint(config tailscale.ServeConfig, name, target string, ports []int, previous state.TailscaleServe, wasOn bool) endpointPlan {
	var plan endpointPlan
	for _, port := range ports {
		endpoint := config.Endpoint(name, port, target)
		recorded := wasOn && port == previous.HTTPSPort
		// After a rename OwnGit's earlier endpoint is under the old name,
		// which answers for nothing and does not take the port for the new
		// one (tailscale.Endpoint.Stale). Under the same name, the recorded
		// endpoint to an earlier target is replaced only when OwnGit created
		// it. One the owner made (Created false) is the owner's Serve
		// setting: OwnGit uses it while it points at OwnGit exactly, and
		// never rewrites it.
		earlier := recorded && previous.Name == name && config.Endpoint(previous.Name, previous.HTTPSPort, previous.Target).Exact
		plan.port = port
		switch {
		case endpoint.Exact && recorded:
			plan.outcome = endpointKept
			return plan
		case endpoint.Exact:
			plan.outcome, plan.found = TailscaleProblemUnrecorded, endpoint.Found
			return plan
		case earlier && previous.Created:
			plan.outcome, plan.found, plan.replaces = endpointCreated, nil, true
			return plan
		case earlier:
			plan.outcome, plan.found = TailscaleProblemOwnersEndpoint, nil
			return plan
		case endpoint.Free:
			plan.outcome, plan.found = endpointCreated, nil
			return plan
		}
		plan.passed = append(plan.passed, port)
		plan.found = append(plan.found, endpoint.Found...)
	}
	plan.port, plan.outcome = 0, TailscaleProblemTaken
	return plan
}

// httpsPorts lists the HTTPS ports that turning on tries, in order: the
// port the owner named; the recorded port while sharing is on under the
// same name, so its address stays as it is; the recorded port first after
// a rename or an interrupted change; otherwise tailscaleHTTPSPorts.
func httpsPorts(named int, previous state.TailscaleServe, wasOn bool, name string) []int {
	switch {
	case named != 0:
		return []int{named}
	case wasOn && previous.Confirmed && previous.Name == name:
		return []int{previous.HTTPSPort}
	case wasOn:
		ports := []int{previous.HTTPSPort}
		for _, port := range tailscaleHTTPSPorts {
			if port != previous.HTTPSPort {
				ports = append(ports, port)
			}
		}
		return ports
	}
	return tailscaleHTTPSPorts
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
	switch report.Endpoint {
	case TailscaleEndpointMissing:
		waiting = append(waiting, TailscaleWaitEndpoint)
	case TailscaleEndpointChanged:
		waiting = append(waiting, TailscaleWaitChanged)
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
	// Target is OwnGit's local address that its endpoint passes requests
	// to, and Port its HTTPS port, for the steps that undo a changed or
	// unrecorded endpoint.
	Target string
	Port   int
	// MacApp is true for the Tailscale app for macOS.
	MacApp bool
}

// Problems of turning sharing on or off, besides the tailscale.Kind values.
const (
	// TailscaleProblemTaken: something else uses every HTTPS port that
	// turning on tried.
	TailscaleProblemTaken = "port_taken"
	// TailscaleProblemOtherPort: sharing is on at another HTTPS port than
	// the one named; turning it off first moves it.
	TailscaleProblemOtherPort = "other_port"
	// TailscaleProblemUnrecorded: OwnGit's exact endpoint is there without
	// a record of OwnGit making it.
	TailscaleProblemUnrecorded = "unrecorded"
	// TailscaleProblemReadBack: Tailscale did not keep the endpoint.
	TailscaleProblemReadBack = "read_back"
	// TailscaleProblemChanged: the endpoint changed since OwnGit made it.
	TailscaleProblemChanged = "endpoint_changed"
	// TailscaleProblemListenOption: the running server listens, by a start
	// option, where Tailscale cannot connect.
	TailscaleProblemListenOption = "listen_option"
	// TailscaleProblemNotOn: sharing is not on.
	TailscaleProblemNotOn = "not_on"
	// TailscaleProblemServeChanged: something else changed Tailscale's
	// Serve configuration between OwnGit's read and its change, so
	// Tailscale did not apply the change.
	TailscaleProblemServeChanged = string(tailscale.KindServeChanged)
	// TailscaleProblemOwnersEndpoint: sharing uses an endpoint the owner
	// made, which passes requests to OwnGit's earlier local address; OwnGit
	// does not rewrite it. Detail is OwnGit's local address now.
	TailscaleProblemOwnersEndpoint = "owners_endpoint"
)

// Refused reports whether the problem is a state the owner can fix, such as
// Tailscale being signed out or a port being taken. Such a refusal is
// answered as refused input (409). A timeout, an answer OwnGit cannot read,
// a failure Tailscale did not explain in a way OwnGit recognizes
// (tailscale.classify), or a change Tailscale accepted and did not keep may
// pass on another try, so it is work that could not be completed now (503,
// logged). A change not kept by the Tailscale app for macOS still gets that
// app's fix on the page (MsgTSReadBackMacApp).
func (err *TailscaleError) Refused() bool {
	switch err.Problem {
	case string(tailscale.KindTimeout), string(tailscale.KindUnreadable), string(tailscale.KindFailed), TailscaleProblemReadBack:
		return false
	}
	return true
}

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
	// PassedPorts are the HTTPS ports that turning on passed over because
	// something else uses them, before the one in Record.
	PassedPorts []int
}

// On turns sharing on. homeNetwork chooses the listen address: nil keeps it
// when Tailscale can reach it and otherwise listens on this computer only,
// true also listens on the home network, and false listens on this computer
// only. Changing the listen address applies at the next start. port is the
// HTTPS port to use, or 0 to use the recorded one or the first free one of
// tailscaleHTTPSPorts.
func (sharing *Tailscale) On(ctx context.Context, homeNetwork *bool, port int) (TailscaleChange, error) {
	ctx, unlock, err := sharing.lock(ctx)
	if err != nil {
		return TailscaleChange{}, err
	}
	defer unlock()
	defer sharing.forget()
	change, err := sharing.on(ctx, homeNetwork, port)
	if err == nil && sharing.Live != nil {
		sharing.Live.ApplyTailscale(change.Record, change.RemovedHost)
	}
	return change, err
}

// lock starts a change: it runs to its end even when ctx is cancelled,
// within tailscaleChangeTimeout and before ctx's deadline (see
// tailscaleChangeTimeout), and after every other change of this state
// directory. A call to Tailscale cut at that deadline has an unknown
// outcome, which the change reports as such (ErrTailscaleAhead) and the next
// change finishes or undoes.
func (sharing *Tailscale) lock(ctx context.Context) (context.Context, func(), error) {
	now := time.Now()
	deadline := now.Add(tailscaleChangeTimeout)
	if requestDeadline, ok := ctx.Deadline(); ok {
		answer := requestDeadline.Add(-min(tailscaleAnswerReserve, requestDeadline.Sub(now)/2))
		if answer.Before(deadline) {
			deadline = answer
		}
	}
	ctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
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

func (sharing *Tailscale) on(ctx context.Context, homeNetwork *bool, httpsPort int) (TailscaleChange, error) {
	command, err := sharing.Find()
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

	// Read before writing: the port must be free, or hold exactly the
	// endpoint this record describes (planEndpoint).
	config, err := command.ServeConfig(ctx)
	if err != nil {
		return TailscaleChange{}, tailscaleError(err, command.MacApp)
	}
	// Sharing that is on moves to another port only by turning it off
	// first, which removes the endpoint on the old one. The same holds for
	// an interrupted turning on whose endpoint is still there: a record for
	// the new port would replace the only record of it.
	if httpsPort != 0 && wasOn && previous.Name == status.Name && httpsPort != previous.HTTPSPort &&
		(previous.Confirmed || previous.Created && config.Endpoint(previous.Name, previous.HTTPSPort, previous.Target).Exact) {
		return TailscaleChange{}, &TailscaleError{Problem: TailscaleProblemOtherPort, Detail: TailscaleOrigin(previous.Name, previous.HTTPSPort) + "/"}
	}
	plan := planEndpoint(config, status.Name, target, httpsPorts(httpsPort, previous, wasOn, status.Name), previous, wasOn)
	// A record whose settings were never saved belongs to a turning on that
	// was interrupted before it changed any setting, so this one starts
	// anew: it records the base URL saved now and what it adds itself.
	fresh := !wasOn || !previous.SavedSettings()
	record := previous
	if fresh {
		record = state.TailscaleServe{CreatedAt: time.Now().Unix()}
	}
	record.Name, record.HTTPSPort, record.Target = status.Name, plan.port, target
	change.Endpoint, change.PassedPorts = plan.outcome, plan.passed
	switch plan.outcome {
	case endpointKept:
		// What OwnGit created belongs to the name it was created for. After
		// a rename the endpoint for the new name was not made by this
		// record, so turning off leaves it.
		record.Created = previous.Created && previous.Name == status.Name
	case endpointCreated:
		record.Created = true
	case TailscaleProblemOwnersEndpoint:
		return TailscaleChange{}, &TailscaleError{Problem: plan.outcome, Detail: target, Port: plan.port}
	default:
		return TailscaleChange{}, &TailscaleError{Problem: plan.outcome, Found: plan.found, Port: plan.port}
	}

	if change.Endpoint == endpointCreated {
		// Every endpoint OwnGit writes is recorded as its own, not yet
		// confirmed, before it is written. An endpoint that is written and
		// then interrupted, or whose settings are not saved, is then still
		// OwnGit's for the next turning on or off, which finishes or undoes
		// the change. A record whose settings were saved keeps naming them.
		pending := record
		pending.Confirmed = false
		// An endpoint OwnGit created on this name and port is already
		// recorded as its own. Replacing it with a new target
		// (plan.replaces), the pending record keeps the target it has now:
		// whether or not the write took effect, the next turning on then
		// recognises the port as OwnGit's (planEndpoint), and turning off
		// removes the endpoint that is still there when it did not.
		if plan.replaces {
			pending.Target = previous.Target
		}
		if err := sharing.Store.SaveTailscaleServe(ctx, pending); err != nil {
			return TailscaleChange{}, err
		}
		// restore puts back the record from before, once the endpoint is
		// known not to be written, and returns cause. When the record
		// cannot be put back, the pending one stays and may name an endpoint
		// OwnGit did not write, so that failure is returned instead, with
		// cause in its text: it is not the refusal cause may be.
		restore := func(cause error) error {
			var err error
			if wasOn {
				err = sharing.Store.SaveTailscaleServe(ctx, previous)
			} else {
				err = sharing.Store.ClearTailscaleServe(ctx)
			}
			if err != nil {
				return fmt.Errorf("%v, and the sharing record from before could not be put back: %w", cause, err)
			}
			return cause
		}
		if sharing.BeforeServe != nil {
			sharing.BeforeServe(record.Name)
		}
		writeErr := whyWriteFailed(ctx, command, command.ServeHTTPS(ctx, config, record.Name, record.HTTPSPort, target))
		after, readErr := command.ServeConfig(ctx)
		shown := readErr == nil && after.Endpoint(record.Name, record.HTTPSPort, target).Exact
		if writeErr == nil && readErr == nil && !shown {
			writeErr = &TailscaleError{Problem: TailscaleProblemReadBack, MacApp: command.MacApp}
		}
		ahead := tailscaleMayHave(writeErr, readErr, shown)
		if writeErr == nil {
			writeErr = readErr
		}
		if writeErr != nil {
			err := tailscaleAhead(tailscaleError(writeErr, command.MacApp), ahead)
			if !ahead {
				err = restore(err)
			}
			return TailscaleChange{}, err
		}
	}

	written := change.Endpoint == endpointCreated
	update, err := sharing.onUpdate(ctx, saved, change.Listen, &record, fresh)
	if err != nil {
		return TailscaleChange{}, tailscaleAhead(err, written)
	}
	if len(update.RemoveHosts) > 0 {
		change.RemovedHost = previous.AddedHost
	}
	// Plain HTTP is accepted only when this change opens the home network.
	host, _, _ := net.SplitHostPort(change.Listen)
	update.AcknowledgeInsecureHTTP = change.ListenChanged && !IsLoopbackHost(host)
	if err := sharing.Store.UpdateNetwork(ctx, update); err != nil {
		return TailscaleChange{}, tailscaleAhead(err, written)
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
	origin := TailscaleOrigin(record.Name, record.HTTPSPort)
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
		command, err := sharing.Find()
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
			removeErr := whyWriteFailed(ctx, command, command.RemoveHTTPS(ctx, config, record.Name, record.HTTPSPort))
			after, readErr := command.ServeConfig(ctx)
			shown := readErr == nil && !after.Endpoint(record.Name, record.HTTPSPort, record.Target).Exact
			if removeErr == nil && readErr == nil && !shown {
				removeErr = &TailscaleError{Problem: TailscaleProblemReadBack, MacApp: command.MacApp}
			}
			ahead := tailscaleMayHave(removeErr, readErr, shown)
			if removeErr == nil {
				removeErr = readErr
			}
			if removeErr != nil {
				return TailscaleChange{}, "", tailscaleAhead(tailscaleError(removeErr, command.MacApp), ahead)
			}
			change.Endpoint = "removed"
		case endpoint.Free:
			change.Endpoint = "gone"
		default:
			return TailscaleChange{}, "", &TailscaleError{Problem: TailscaleProblemChanged, Found: endpoint.Found, Target: record.Target, Port: record.HTTPSPort}
		}
	}

	removed := change.Endpoint == "removed"
	saved, err := sharing.Store.NetworkSettings(ctx)
	if err != nil {
		return TailscaleChange{}, "", tailscaleAhead(err, removed)
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
			return TailscaleChange{}, "", tailscaleAhead(err, removed)
		}
		update.RemoveHosts = matchingHosts(hosts, record.AddedHost)
	}
	if err := sharing.Store.UpdateNetwork(ctx, update); err != nil {
		return TailscaleChange{}, "", tailscaleAhead(err, removed)
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

// throughTailnet reports whether a plain HTTP request came to OwnGit
// directly over the tailnet: from a Tailscale address to one of this
// computer's own Tailscale addresses, as Tailscale reports them. Tailscale
// then encrypted it between the tailnet device that sent it and this
// computer. The address ranges alone do not show that, since other private
// networks and some carriers use 100.64.0.0/10 too. A request from this
// computer itself, or from a trusted proxy with or without forwarding
// headers, is not counted, and neither is Tailscale in userspace networking
// mode, which connects from 127.0.0.1.
func (app *App) throughTailnet(request *http.Request) bool {
	info := requestctx.Of(request)
	if info.Secure() || info.FromProxy {
		return false
	}
	// The address the connection reached, as the listener reports it; a
	// request built by a test has none.
	address, _ := request.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if address == nil {
		return false
	}
	local, err := netip.ParseAddrPort(address.String())
	if err != nil || !tailscale.InTailnetRange(local.Addr()) {
		return false
	}
	peer, err := netip.ParseAddrPort(info.Peer)
	if err != nil || !tailscale.InTailnetRange(peer.Addr()) {
		return false
	}
	addresses := app.Tailscale.addresses()
	return slices.Contains(addresses, local.Addr().Unmap()) && !slices.Contains(addresses, peer.Addr().Unmap())
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

// whyWriteFailed names the cause of a failed Serve change when Tailscale's
// state explains it, such as Tailscale being stopped: Tailscale may accept
// reading its configuration then and refuse only the change. What Tailscale
// printed stays as the detail for the administrator.
func whyWriteFailed(ctx context.Context, command tailscale.Command, err error) error {
	var failure *tailscale.Error
	if !errors.As(err, &failure) || failure.Kind != tailscale.KindFailed {
		return err
	}
	status, statusErr := command.Status(ctx)
	if statusErr != nil {
		return err
	}
	switch kind := tailscale.KindOf(status.Usable()); kind {
	case tailscale.KindStopped, tailscale.KindLoggedOut, tailscale.KindNeedsApproval, tailscale.KindNotRunning:
		return &tailscale.Error{Kind: kind, Detail: failure.Detail}
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
