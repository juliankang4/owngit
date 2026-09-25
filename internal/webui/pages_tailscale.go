package webui

// Actions of the Tailscale block of the Settings page. Both ask for the
// administrator password.
const (
	// ActionTailscaleOn turns sharing on the tailnet on. Fields:
	// admin_password, and home_network when OwnGit should also listen on
	// the home network.
	ActionTailscaleOn = "tailscale_on"
	// ActionTailscaleOff turns it off. Fields: admin_password.
	ActionTailscaleOff = "tailscale_off"
)

// TailscaleInfo is the Tailscale block of the Settings page: sharing OwnGit
// on the tailnet over HTTPS with Tailscale Serve. Every Settings viewer sees
// it; turning sharing on or off asks for the administrator password.
type TailscaleInfo struct {
	// On says that sharing is on, and Ready that its address works.
	On, Ready bool
	// URL is the HTTPS address while sharing is on, such as
	// https://box.tail1234.ts.net/.
	URL string
	// Name is this computer's MagicDNS name, shown in the certificate log
	// notice.
	Name string
	// Problem, when set, says what keeps Tailscale from serving this
	// computer over HTTPS, and ProblemDetail what Tailscale printed.
	Problem       MessageCode
	ProblemDetail string
	// Waiting lists why the address does not work yet while sharing is on.
	Waiting []MessageCode
	// Found lists what else is on Tailscale's HTTPS port, and FoundNote
	// introduces it: the port is taken while sharing is off, or changed
	// while it is on.
	Found []TailscaleUse
	// Stale lists what Tailscale keeps under an earlier name of this
	// computer (MsgTSStale).
	Stale     []TailscaleUse
	FoundNote MessageCode
	// MacApp is true for the Tailscale app for macOS, which does not run
	// after a restart until someone logs in.
	MacApp bool
	// CanTurnOn is true when sharing is off and Tailscale is ready for it,
	// or when sharing is on and waits to be turned on again.
	CanTurnOn bool
	// HomeNetwork is the initial state of the home network checkbox: true
	// when OwnGit already listens on every network.
	HomeNetwork bool
	// HomeListen and LocalListen are the listen addresses with and without
	// the home network, for the checkbox's help text.
	HomeListen, LocalListen string
}
