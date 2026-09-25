package server

import (
	"net/netip"
	"slices"
	"sync"

	"owngit/internal/requestctx"
	"owngit/internal/state"
)

// LiveNetwork holds the network values of the running server that turning
// Tailscale sharing on or off changes without a restart: the base URL, the
// trusted proxies and the accepted Host names, together with the running
// record that reports them. serve creates it. Without one, as in most tests,
// App.Requests and App.BaseURL apply unchanged.
//
// A value the server took from a start option is never changed: the option
// wins for that run, as it does over every saved value.
type LiveNetwork struct {
	mu      sync.RWMutex
	record  state.RunningNetwork
	baseURL string
	proxies []netip.Prefix
	hosts   *HostPolicy
	// flagHosts are the normalized names given with --allowed-host.
	flagHosts []string
	publish   func(state.RunningNetwork)
	// tailscale is the MagicDNS name of the Tailscale endpoint this server
	// serves, or "".
	tailscale string
}

// LiveNetworkConfig is what a serve run starts with.
type LiveNetworkConfig struct {
	// Record is the running record without AcceptedHosts, which come from
	// Hosts each time the record is published.
	Record state.RunningNetwork
	// BaseURL is the canonical configured origin, or "".
	BaseURL   string
	Proxies   []netip.Prefix
	Hosts     *HostPolicy
	FlagHosts []string
	// Publish stores the record; nil when this run may not publish one
	// because it does not hold the running-record lock.
	Publish func(state.RunningNetwork)
	// Tailscale is the confirmed Tailscale sharing record at start, if any.
	Tailscale *state.TailscaleServe
}

// NewLiveNetwork returns the live network of one serve run.
func NewLiveNetwork(config LiveNetworkConfig) *LiveNetwork {
	live := &LiveNetwork{
		record: config.Record, baseURL: config.BaseURL, proxies: slices.Clone(config.Proxies),
		hosts: config.Hosts, flagHosts: NormalizedHosts(config.FlagHosts), publish: config.Publish,
	}
	if config.Tailscale != nil && config.Tailscale.Confirmed {
		live.tailscale = config.Tailscale.Name
	}
	return live
}

// Resolver derives each request's scheme, Host and client address with the
// proxies trusted now.
func (live *LiveNetwork) Resolver() requestctx.Resolver {
	live.mu.RLock()
	defer live.mu.RUnlock()
	return requestctx.Resolver{TrustedProxies: live.proxies, HostAllowed: live.hosts.Allows}
}

// BaseURL is the configured origin the server uses now, or "".
func (live *LiveNetwork) BaseURL() string {
	live.mu.RLock()
	defer live.mu.RUnlock()
	return live.baseURL
}

// TailscaleName is the MagicDNS name of the Tailscale endpoint in use, or "".
func (live *LiveNetwork) TailscaleName() string {
	live.mu.RLock()
	defer live.mu.RUnlock()
	return live.tailscale
}

// Publish records what the server uses now, with the Host names the policy
// accepts now.
func (live *LiveNetwork) Publish() {
	live.mu.RLock()
	record := live.record
	live.mu.RUnlock()
	if live.publish == nil {
		return
	}
	record.SavedHosts = slices.Clone(record.SavedHosts)
	record.TrustedProxies = slices.Clone(record.TrustedProxies)
	record.AcceptedHosts = live.hosts.Hosts()
	live.publish(record)
}

// ApplyTailscale makes the running server do what the saved settings of
// Tailscale sharing say: accept the name, trust 127.0.0.1 as a proxy and use
// the HTTPS base URL, except where a start option decides.
func (live *LiveNetwork) ApplyTailscale(record state.TailscaleServe) {
	live.mu.Lock()
	loopback := netip.MustParsePrefix(loopbackProxy + "/32")
	if live.record.TrustedProxiesSource != NetworkSourceFlag && !slices.ContainsFunc(live.proxies, func(prefix netip.Prefix) bool { return prefix.Contains(loopback.Addr()) }) {
		live.proxies = append(slices.Clone(live.proxies), loopback)
		live.record.TrustedProxies = sortedWith(live.record.TrustedProxies, loopbackProxy)
		live.record.TrustedProxiesSource = NetworkSourceSaved
	}
	if live.record.BaseURLSource != NetworkSourceFlag {
		live.baseURL, live.record.BaseURL, live.record.Origin = record.BaseURL, record.BaseURL, record.BaseURL
		live.record.BaseURLSource = NetworkSourceSaved
	}
	_ = live.hosts.Add(record.Name)
	live.tailscale = record.Name
	live.mu.Unlock()
	live.Publish()
}

// RemoveTailscale takes back what ApplyTailscale, or the saved settings at
// start, did for record, and uses baseURL, the base URL saved now, except
// where a start option decides.
func (live *LiveNetwork) RemoveTailscale(record state.TailscaleServe, baseURL string) {
	live.mu.Lock()
	if record.AddedProxy != "" && live.record.TrustedProxiesSource != NetworkSourceFlag {
		if prefix, err := requestctx.ParseTrustedProxy(record.AddedProxy); err == nil {
			live.proxies = slices.DeleteFunc(slices.Clone(live.proxies), func(value netip.Prefix) bool { return value == prefix })
			live.record.TrustedProxies = slices.DeleteFunc(slices.Clone(live.record.TrustedProxies), func(value string) bool { return value == record.AddedProxy })
			if len(live.record.TrustedProxies) == 0 {
				live.record.TrustedProxiesSource = NetworkSourceDefault
			}
		}
	}
	if live.record.BaseURLSource != NetworkSourceFlag {
		live.baseURL, live.record.BaseURL, live.record.BaseURLSource = baseURL, baseURL, NetworkSourceSaved
		live.record.Origin = baseURL
		if baseURL == "" {
			live.record.BaseURLSource, live.record.Origin = NetworkSourceDefault, "http://"+live.record.Address
		}
	}
	if record.AddedHost != "" && !slices.Contains(live.flagHosts, record.AddedHost) {
		live.hosts.Remove(record.AddedHost)
		live.record.SavedHosts = slices.DeleteFunc(slices.Clone(live.record.SavedHosts), func(host string) bool { return host == record.AddedHost })
	}
	live.tailscale = ""
	live.mu.Unlock()
	live.Publish()
}

// sortedWith returns values with value added, sorted and without
// duplicates.
func sortedWith(values []string, value string) []string {
	values = slices.Clone(values)
	if !slices.Contains(values, value) {
		values = append(values, value)
	}
	slices.Sort(values)
	return values
}
