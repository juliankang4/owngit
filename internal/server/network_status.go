package server

import (
	"slices"

	"owngit/internal/state"
)

// Where a network value came from, in state.RunningNetwork and NetworkReport.
const (
	NetworkSourceFlag    = "flag"
	NetworkSourceSaved   = "saved"
	NetworkSourceDefault = "default"
)

// Server states in NetworkReport; see state.RunningObservation.
const (
	NetworkRunning    = state.ServerRunning
	NetworkStarting   = state.ServerStarting
	NetworkNotRunning = state.ServerNotRunning
	NetworkUnknown    = state.ServerUnknown
)

// NetworkReport compares the saved network settings with what the running
// server uses. "owngit network show --json" prints it, and the Settings page
// renders it, so both tell saved from running values by the same rules.
type NetworkReport struct {
	Saved struct {
		Listen         string   `json:"listen"`
		BaseURL        string   `json:"base_url"`
		AllowedHosts   []string `json:"allowed_hosts"`
		TrustedProxies []string `json:"trusted_proxies"`
	} `json:"saved"`
	// NextStart is what a start without network flags uses.
	NextStart struct {
		Listen        string `json:"listen"`
		ListenSource  string `json:"listen_source"`
		BaseURL       string `json:"base_url"`
		BaseURLSource string `json:"base_url_source"`
		// TrustedProxies is the saved list; a serve flag replaces it.
		TrustedProxies       []string `json:"trusted_proxies"`
		TrustedProxiesSource string   `json:"trusted_proxies_source"`
	} `json:"next_start"`
	// Server is one of NetworkRunning, NetworkStarting, NetworkNotRunning
	// and NetworkUnknown.
	Server        string                `json:"server"`
	Running       *state.RunningNetwork `json:"running"`
	RestartNeeded bool                  `json:"restart_needed"`
	// StaleRecord says that a record left by a server that ended without
	// cleanup was found and ignored.
	StaleRecord bool `json:"stale_record"`
}

// NewNetworkReport fills the saved and next-start values. hosts are the
// stored allowed Host names and proxies the saved trusted proxies. The
// server state is set with SetServer.
func NewNetworkReport(saved state.NetworkSettings, hosts, proxies []string) NetworkReport {
	var report NetworkReport
	report.Saved.Listen, report.Saved.BaseURL = saved.Listen, saved.BaseURL
	report.Saved.AllowedHosts = NormalizedHosts(hosts)
	report.Saved.TrustedProxies = nonNil(proxies)
	report.NextStart.TrustedProxies, report.NextStart.TrustedProxiesSource = report.Saved.TrustedProxies, NetworkSourceDefault
	if len(proxies) > 0 {
		report.NextStart.TrustedProxiesSource = NetworkSourceSaved
	}
	report.NextStart.Listen, report.NextStart.ListenSource = DefaultListenAddress, NetworkSourceDefault
	if saved.Listen != "" {
		report.NextStart.Listen, report.NextStart.ListenSource = saved.Listen, NetworkSourceSaved
	}
	report.NextStart.BaseURL, report.NextStart.BaseURLSource = saved.BaseURL, NetworkSourceDefault
	if saved.BaseURL != "" {
		report.NextStart.BaseURLSource = NetworkSourceSaved
	}
	report.Server = NetworkNotRunning
	return report
}

// SetServer records the server state from an observation made by
// state.ObserveRunningNetwork or state.OwnRunningNetwork, which trust the
// running record only while its publisher is alive.
func (report *NetworkReport) SetServer(observed state.RunningObservation) {
	report.Server, report.Running, report.StaleRecord = observed.Server, observed.Record, observed.StaleRecord
	report.RestartNeeded = report.Pending().Any()
}

// NetworkPending names the saved values the running server does not use yet.
type NetworkPending struct {
	Listen, BaseURL, Hosts, Proxies bool
}

// Any reports whether a restart is needed for the saved values to apply.
func (pending NetworkPending) Any() bool {
	return pending.Listen || pending.BaseURL || pending.Hosts || pending.Proxies
}

// Pending compares the saved values with the running record. It is all
// false unless the report has a trusted running record. A value the server
// took from a flag is not compared: the flag wins for that run whatever is
// saved. A saved Host name is pending when the running server does not
// accept it yet, and a removed one when the server loaded it at start.
func (report NetworkReport) Pending() NetworkPending {
	running := report.Running
	if running == nil {
		return NetworkPending{}
	}
	var pending NetworkPending
	pending.Listen = running.ListenSource != NetworkSourceFlag && report.NextStart.Listen != running.Listen
	pending.BaseURL = running.BaseURLSource != NetworkSourceFlag && report.NextStart.BaseURL != running.BaseURL
	for _, host := range report.Saved.AllowedHosts {
		if !slices.Contains(running.AcceptedHosts, host) {
			pending.Hosts = true
		}
	}
	for _, host := range running.SavedHosts {
		if !slices.Contains(report.Saved.AllowedHosts, host) {
			pending.Hosts = true
		}
	}
	pending.Proxies = running.TrustedProxiesSource != NetworkSourceFlag &&
		!slices.Equal(report.Saved.TrustedProxies, nonNil(running.TrustedProxies))
	return pending
}

// NormalizedHosts returns stored Host names in the form the Host check uses,
// sorted and without duplicates. A name that cannot be normalized is kept as
// stored, so it stays visible.
func NormalizedHosts(values []string) []string {
	hosts := []string{}
	for _, value := range values {
		host, err := NormalizeHost(value)
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

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
