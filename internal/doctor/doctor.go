// Package doctor finds what keeps an OwnGit installation from working on
// this computer and names one repair for each problem. "owngit doctor" and
// the Settings page show the same findings from the same facts.
//
// A finding names only what OwnGit read on this computer: whether the
// server answers, its setup, the owner of its folders, and the firewall
// configuration of this computer that it knows. A check that could not
// run, or whose answer OwnGit cannot read, is a finding too, never a clean
// result.
package doctor

import (
	"net"
	"net/netip"
	"strings"

	"owngit/internal/service"
	"owngit/internal/webui"
)

// Facts is what the checks read about one installation.
type Facts struct {
	// GOOS is the operating system, as runtime.GOOS names it.
	GOOS string
	// Server says whether the server of this state directory runs.
	Server Server
	// ServerReason is why Server is ServerUnknown, as the system said it.
	ServerReason string
	// Service is true when a service of this account runs OwnGit.
	Service bool
	// SetupComplete is true when the owner finished setup. It is read only
	// while the server runs.
	SetupComplete bool
	// Listen is the address the server listens on, or will listen on by
	// its saved setting, as host:port.
	Listen string
	// OtherDevices is true when Listen reaches beyond this computer.
	OtherDevices bool
	// Program is the owngit program that serves.
	Program string
	// Administrator is true on Windows when the account that runs OwnGit
	// is an administrator. Its "owngit service install" adds OwnGit's
	// firewall rule with one approval.
	Administrator bool
	// System is the Windows System32 folder.
	System string
	// AdministratorsFolders are the state and repository folders that the
	// Administrators group owns on Windows.
	AdministratorsFolders []string
	// Firewall is the firewall configuration of this computer.
	Firewall Firewall
	// Unchecked names each check that could not run, with why.
	Unchecked []Unchecked
}

// Server is what OwnGit read about the server of the state directory.
type Server int

const (
	// ServerStopped means no server uses the state directory and nothing
	// answers at its address.
	ServerStopped Server = iota
	// ServerRunning means the server of the state directory runs and
	// answers.
	ServerRunning
	// ServerSilent means the state directory says its server runs, but
	// nothing answers at its address.
	ServerSilent
	// ServerElsewhere means no server uses the state directory, but
	// another program answers at its address.
	ServerElsewhere
	// ServerUnknown means the state directory could not tell.
	ServerUnknown
)

// Firewall is what OwnGit read of this computer's firewall. Only the part
// of the running operating system is filled.
type Firewall struct {
	// Windows: OwnGit's rule, and which networks let devices reach the
	// program on the listen port.
	Rule   Rule
	Access service.FirewallAccess
	// macOS: the application firewall is on, blocks every incoming
	// connection, or blocks Program.
	AppFirewall bool
	BlockAll    bool
	AppBlocked  bool
	// Linux: ufw is enabled, or firewalld runs. Their rules need root to
	// read.
	UFW       bool
	Firewalld bool
	// Linux: the private networks of the interfaces that OwnGit listens
	// on, each with the firewalld zone of its interface.
	Private []Network
}

// Network is a private network that OwnGit listens on.
type Network struct {
	Prefix netip.Prefix
	// Zone is the firewalld zone of the network's interface, or "" when
	// firewalld did not say.
	Zone string
}

// Rule is the state of OwnGit's Windows Firewall rule for Program.
type Rule int

const (
	// RuleMissing means no rule named OwnGit exists.
	RuleMissing Rule = iota
	// RuleAllows means OwnGit's rule lets Program in on private networks.
	RuleAllows
	// RuleOther means OwnGit's rule names another program or setting.
	RuleOther
	// RuleForeign means a rule named OwnGit exists that OwnGit did not add.
	RuleForeign
)

// Unchecked is a check that could not run.
type Unchecked struct {
	// Code says which check; Reason says why, as the system reported it.
	Code   webui.MessageCode
	Reason string
}

// Windows network profiles (NET_FW_PROFILE_TYPE2).
const (
	domainProfile  = 1
	privateProfile = 2
	publicProfile  = 4
)

// Diagnose returns the findings for facts, most basic first. A server that
// does not run and answer is the only finding, since the other checks
// describe a running server.
func Diagnose(facts Facts) []webui.Finding {
	switch facts.Server {
	case ServerStopped:
		repair := "owngit service install"
		if facts.Service {
			repair = "owngit service start"
		}
		return []webui.Finding{{Code: webui.MsgDoctorNotRunning, Repair: repair}}
	case ServerSilent:
		finding := webui.Finding{Code: webui.MsgDoctorSilent, Args: []string{facts.Listen}}
		if facts.Service {
			finding.Repair = "owngit service restart"
		}
		return []webui.Finding{finding}
	case ServerElsewhere:
		return []webui.Finding{{Code: webui.MsgDoctorAddressTaken, Args: []string{facts.Listen}}}
	case ServerUnknown:
		return []webui.Finding{{Code: webui.MsgDoctorUncheckedServer, Args: []string{facts.ServerReason}, Unchecked: true}}
	}
	var findings []webui.Finding
	if !facts.SetupComplete {
		findings = append(findings, webui.Finding{Code: webui.MsgDoctorSetup, Repair: "owngit setup-link"})
	}
	for _, folder := range facts.AdministratorsFolders {
		findings = append(findings, webui.Finding{Code: webui.MsgDoctorAdministratorsFolder, Args: []string{folder}, Repair: "owngit service install"})
	}
	if facts.OtherDevices {
		findings = append(findings, firewallFindings(facts)...)
	}
	for _, unchecked := range facts.Unchecked {
		findings = append(findings, webui.Finding{Code: unchecked.Code, Args: []string{unchecked.Reason}, Unchecked: true})
	}
	return findings
}

// firewallFindings names the firewall configuration of this computer that
// keeps other devices out, or may.
func firewallFindings(facts Facts) []webui.Finding {
	firewall := facts.Firewall
	_, port, _ := net.SplitHostPort(facts.Listen)
	switch facts.GOOS {
	case "windows":
		access := firewall.Access
		closed := access.Closed()
		switch {
		case closed == 0:
		case closed&access.BlockAll != 0:
			return []webui.Finding{{Code: webui.MsgDoctorWindowsBlockAll}}
		case closed&access.Blocked != 0:
			return []webui.Finding{{Code: webui.MsgDoctorWindowsBlocked, Args: []string{facts.Program}}}
		// A rule lets devices in on private networks, and the network in
		// use is another kind.
		case access.Allowed&privateProfile != 0 && closed&privateProfile == 0:
			if closed&publicProfile != 0 {
				return []webui.Finding{{Code: webui.MsgDoctorWindowsPublic}}
			}
			return []webui.Finding{{Code: webui.MsgDoctorWindowsDomain}}
		case firewall.Rule == RuleForeign:
			return []webui.Finding{{Code: webui.MsgDoctorWindowsForeignRule}}
		case facts.Administrator:
			return []webui.Finding{{Code: webui.MsgDoctorWindowsRule, Args: []string{facts.Program}, Repair: "owngit service install"}}
		default:
			// PowerShell runs the command as printed: every path is one
			// literal argument.
			netsh := strings.Join([]string{
				"&", service.PowerShellQuote(facts.System + `\netsh.exe`), "advfirewall", "firewall", "add", "rule",
				service.PowerShellQuote("name=OwnGit on private networks"), "dir=in", "action=allow", "profile=private",
				service.PowerShellQuote("program=" + facts.Program),
			}, " ")
			return []webui.Finding{{Code: webui.MsgDoctorWindowsRuleAsk, Args: []string{facts.Program}, Repair: netsh}}
		}
	case "darwin":
		switch {
		case !firewall.AppFirewall:
		case firewall.BlockAll:
			return []webui.Finding{{Code: webui.MsgDoctorMacBlockAll}}
		case firewall.AppBlocked:
			return []webui.Finding{{Code: webui.MsgDoctorMacBlocked, Args: []string{facts.Program},
				Repair: "sudo /usr/libexec/ApplicationFirewall/socketfilterfw --unblockapp " + service.ShellQuote(facts.Program)}}
		}
	case "linux":
		var findings []webui.Finding
		if firewall.UFW {
			findings = append(findings, ufwFinding(firewall.Private, port))
		}
		if firewall.Firewalld {
			findings = append(findings, firewalldFinding(firewall.Private, port))
		}
		return findings
	}
	return nil
}

// The Linux firewall tools, at the paths their packages install them.
const (
	ufwCommand       = "/usr/sbin/ufw"
	firewalldCommand = "/usr/bin/firewall-cmd"
)

// ufwFinding says that ufw decides whether devices reach port, which
// OwnGit cannot read without root. Its repair allows the port from the
// private networks only; without one, the sentence says what to allow.
func ufwFinding(networks []Network, port string) webui.Finding {
	if len(networks) == 0 {
		return webui.Finding{Code: webui.MsgDoctorUFWManual, Args: []string{port}, Unchecked: true}
	}
	var commands []string
	for _, network := range networks {
		commands = append(commands, "sudo "+ufwCommand+" allow from "+network.Prefix.String()+" to any port "+port+" proto tcp")
	}
	return webui.Finding{Code: webui.MsgDoctorUFW, Args: []string{networkList(networks), port}, Repair: strings.Join(commands, " && "), Unchecked: true}
}

// firewalldFinding is ufwFinding for firewalld: the repair adds a rule for
// each private network in the zone of its interface.
func firewalldFinding(networks []Network, port string) webui.Finding {
	manual := webui.Finding{Code: webui.MsgDoctorFirewalldManual, Args: []string{port}, Unchecked: true}
	if len(networks) == 0 {
		return manual
	}
	var commands []string
	for _, network := range networks {
		if network.Zone == "" {
			return manual
		}
		family := "ipv4"
		if network.Prefix.Addr().Is6() {
			family = "ipv6"
		}
		rule := `rule family="` + family + `" source address="` + network.Prefix.String() + `" port port="` + port + `" protocol="tcp" accept`
		commands = append(commands, "sudo "+firewalldCommand+" --permanent "+service.ShellQuote("--zone="+network.Zone)+" "+service.ShellQuote("--add-rich-rule="+rule))
	}
	commands = append(commands, "sudo "+firewalldCommand+" --reload")
	return webui.Finding{Code: webui.MsgDoctorFirewalld, Args: []string{networkList(networks), port}, Repair: strings.Join(commands, " && "), Unchecked: true}
}

// networkList names the networks, as "192.168.1.0/24, fd00::/64".
func networkList(networks []Network) string {
	names := make([]string, len(networks))
	for index, network := range networks {
		names[index] = network.Prefix.String()
	}
	return strings.Join(names, ", ")
}

// PrivateNetworks returns the private networks that a server listening on
// host reaches, from the addresses of this computer's interfaces: every
// private network of an up interface for an every-address listener (IPv4
// only for 0.0.0.0), or the network of the address host names. Loopback,
// link-local, public and shared (100.64.0.0/10, as Tailscale uses)
// addresses are never private networks here.
func PrivateNetworks(host string, interfaces []Interface) []Network {
	listen, err := netip.ParseAddr(host)
	if host != "" && err != nil {
		return nil
	}
	var networks []Network
	for _, link := range interfaces {
		for _, prefix := range link.Prefixes {
			address := prefix.Addr().Unmap()
			if !address.IsPrivate() {
				continue
			}
			switch {
			case host == "" || listen == netip.IPv6Unspecified():
			case listen == netip.IPv4Unspecified():
				if !address.Is4() {
					continue
				}
			case listen.Unmap() != address:
				continue
			}
			network := Network{Prefix: netip.PrefixFrom(address, prefix.Bits()).Masked()}
			if !containsNetwork(networks, network.Prefix) {
				network.Zone = link.Zone
				networks = append(networks, network)
			}
		}
	}
	return networks
}

func containsNetwork(networks []Network, prefix netip.Prefix) bool {
	for _, network := range networks {
		if network.Prefix == prefix {
			return true
		}
	}
	return false
}

// Interface is a network interface of this computer that is up, with its
// addresses and, where firewalld runs, its zone.
type Interface struct {
	Name     string
	Prefixes []netip.Prefix
	Zone     string
}
