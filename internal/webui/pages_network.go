package webui

// ActionSaveNetwork saves the network settings from the Settings page.
// Fields: admin_password, listen, base_url, allowed_hosts, trusted_proxies
// (one entry per line), and network_revision, which names the saved values
// the form was opened with.
const ActionSaveNetwork = "save_network"

// NetworkStatus is the one-line state of the Network block.
type NetworkStatus string

const (
	// NetworkCurrent: the running server uses the saved settings, apart
	// from values given as start options.
	NetworkCurrent NetworkStatus = "current"
	// NetworkRestart: a saved value applies only after a restart.
	NetworkRestart NetworkStatus = "restart"
	// NetworkUnconfirmed: this server cannot vouch for the values it runs
	// with, so only the saved values are shown.
	NetworkUnconfirmed NetworkStatus = "unconfirmed"
)

// Where a value comes from, in NetworkValue.
const (
	NetworkFromSaved   = "saved"
	NetworkFromDefault = "default"
	NetworkFromOption  = "flag"
)

// NetworkInfo is the Network block of the Settings page: every viewer sees
// it, and saving asks for the administrator password. Nothing here changes
// the running server; saved values apply at the next start.
type NetworkInfo struct {
	Status NetworkStatus
	// Listen is this computer's address, BaseURL the address other devices
	// use, Hosts the allowed names and Proxies the trusted reverse proxies.
	Listen, BaseURL, Hosts, Proxies NetworkValue
	// Form holds the text fields: the saved values, or what was submitted
	// when a save was refused.
	Form NetworkForm
	// Revision names the saved values the form shows. A save from a form
	// opened before another change is refused instead of overwriting it.
	Revision string
	// PlainHTTP is true when the next start listens beyond this computer,
	// where other devices reach OwnGit over plain HTTP.
	PlainHTTP bool
	// NeedsName is true when the next start listens on every network but
	// no base URL or allowed name tells other devices a name to use.
	NeedsName bool
	// HTTPSWithoutProxy is true when the saved base URL uses https but no
	// reverse proxy is trusted, the case "owngit network set" warns about.
	HTTPSWithoutProxy bool
	// FromOption is true when the running server took a value from a start
	// option, which overrides the saved value for that run.
	FromOption bool
	// Focus is the form field the refused save should place the reader on.
	Focus string
	// DefaultListen is the listen address without a saved value,
	// EveryNetwork an example that listens on every network, and
	// ProxyExample example trusted proxies. The help text shows them.
	DefaultListen, EveryNetwork, ProxyExample string
}

// NetworkValue compares one setting at the next start with the running
// server.
type NetworkValue struct {
	// Next is what the next start uses; empty means none.
	Next       []string
	NextSource string
	// Now is what the running server uses. NowKnown is false when the
	// server cannot vouch for its running values.
	Now       []string
	NowSource string
	NowKnown  bool
	// Pending is true when the saved value applies only after a restart.
	Pending bool
}

// NetworkForm is the text of the Network form fields.
type NetworkForm struct {
	Listen, BaseURL, Hosts, Proxies string
}

// NetworkRow is one row of the Network block: a setting's label, the text
// shown when it has no value, and its values.
type NetworkRow struct {
	Name, Empty MessageCode
	V           NetworkValue
}

// Rows lists the settings in the order the page shows them.
func (info NetworkInfo) Rows() []NetworkRow {
	return []NetworkRow{
		{Name: MsgNetListen, Empty: MsgNetNone, V: info.Listen},
		{Name: MsgNetBaseURL, Empty: MsgNetNoBaseURL, V: info.BaseURL},
		{Name: MsgNetHosts, Empty: MsgNetNone, V: info.Hosts},
		{Name: MsgNetProxies, Empty: MsgNetNone, V: info.Proxies},
	}
}
