package doctor

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"owngit/internal/service"
	"owngit/internal/webui"
)

// Each problem gets one finding with one repair, a firewall is looked at
// only when OwnGit listens for other devices, and a check that could not
// run, or settings OwnGit cannot read, is a finding marked unchecked, never
// a clean result.
func TestDiagnose(t *testing.T) {
	running := func(goos string) Facts {
		return Facts{GOOS: goos, Server: ServerRunning, SetupComplete: true, Listen: "0.0.0.0:7654", OtherDevices: true,
			Program: `C:\Program Files\OwnGit\owngit.exe`, System: `C:\Windows\System32`}
	}
	with := func(facts Facts, change func(*Facts)) Facts {
		change(&facts)
		return facts
	}
	home := []Network{{Prefix: netip.MustParsePrefix("192.168.50.0/24"), Zone: "home"}}
	// Windows: a network in use (private) with the firewall on.
	private := service.FirewallAccess{Active: 2, On: 7}
	for name, test := range map[string]struct {
		facts Facts
		want  []webui.Finding
	}{
		"not running without a service": {Facts{GOOS: "linux"}, []webui.Finding{{Code: webui.MsgDoctorNotRunning, Repair: "owngit service install"}}},
		"not running with a service": {Facts{GOOS: "linux", Service: true, AdministratorsFolders: []string{"x"}},
			[]webui.Finding{{Code: webui.MsgDoctorNotRunning, Repair: "owngit service start"}}},
		"running by the state, no answer": {Facts{GOOS: "linux", Server: ServerSilent, Service: true, Listen: "127.0.0.1:18961"},
			[]webui.Finding{{Code: webui.MsgDoctorSilent, Args: []string{"127.0.0.1:18961"}, Repair: "owngit service restart"}}},
		"another program at the address": {Facts{GOOS: "linux", Server: ServerElsewhere, Listen: "127.0.0.1:18961"},
			[]webui.Finding{{Code: webui.MsgDoctorAddressTaken, Args: []string{"127.0.0.1:18961"}}}},
		"server unknown": {Facts{GOOS: "linux", Server: ServerUnknown, ServerReason: "held"},
			[]webui.Finding{{Code: webui.MsgDoctorUncheckedServer, Args: []string{"held"}, Unchecked: true}}},
		"setup": {with(running("linux"), func(f *Facts) { f.SetupComplete, f.OtherDevices = false, false }),
			[]webui.Finding{{Code: webui.MsgDoctorSetup, Repair: "owngit setup-link"}}},
		"all well": {running("linux"), nil},
		"this computer only, whatever the firewall": {with(running("linux"), func(f *Facts) { f.OtherDevices, f.Firewall.UFW = false, true }), nil},
		"ufw and firewalld on a private network": {with(running("linux"), func(f *Facts) {
			f.Firewall.UFW, f.Firewall.Firewalld, f.Firewall.Private = true, true, home
		}), []webui.Finding{
			{Code: webui.MsgDoctorUFW, Args: []string{"192.168.50.0/24", "7654"}, Unchecked: true,
				Repair: "sudo /usr/sbin/ufw allow from 192.168.50.0/24 to any port 7654 proto tcp"},
			{Code: webui.MsgDoctorFirewalld, Args: []string{"192.168.50.0/24", "7654"}, Unchecked: true,
				Repair: `sudo /usr/bin/firewall-cmd --permanent '--zone=home' '--add-rich-rule=rule family="ipv4" source address="192.168.50.0/24" port port="7654" protocol="tcp" accept' && sudo /usr/bin/firewall-cmd --reload`},
		}},
		// A public zone still gets only a rule for the private network.
		"firewalld in the public zone": {with(running("linux"), func(f *Facts) {
			f.Firewall.Firewalld = true
			f.Firewall.Private = []Network{{Prefix: netip.MustParsePrefix("fd12:3456::/64"), Zone: "public"}}
		}), []webui.Finding{{Code: webui.MsgDoctorFirewalld, Args: []string{"fd12:3456::/64", "7654"}, Unchecked: true,
			Repair: `sudo /usr/bin/firewall-cmd --permanent '--zone=public' '--add-rich-rule=rule family="ipv6" source address="fd12:3456::/64" port port="7654" protocol="tcp" accept' && sudo /usr/bin/firewall-cmd --reload`}}},
		// Without a private network, or a zone it read, OwnGit gives no
		// command, which could open the port to every network.
		"ufw and firewalld without a private network": {with(running("linux"), func(f *Facts) { f.Firewall.UFW, f.Firewall.Firewalld = true, true }), []webui.Finding{
			{Code: webui.MsgDoctorUFWManual, Args: []string{"7654"}, Unchecked: true},
			{Code: webui.MsgDoctorFirewalldManual, Args: []string{"7654"}, Unchecked: true},
		}},
		"firewalld without the zone": {with(running("linux"), func(f *Facts) {
			f.Firewall.Firewalld, f.Firewall.Private = true, []Network{{Prefix: netip.MustParsePrefix("10.0.0.0/8")}}
		}), []webui.Finding{{Code: webui.MsgDoctorFirewalldManual, Args: []string{"7654"}, Unchecked: true}}},
		"macOS firewall off": {with(running("darwin"), func(f *Facts) { f.Firewall.BlockAll, f.Firewall.AppBlocked = true, true }), nil},
		"macOS blocks all": {with(running("darwin"), func(f *Facts) { f.Firewall.AppFirewall, f.Firewall.BlockAll = true, true }),
			[]webui.Finding{{Code: webui.MsgDoctorMacBlockAll}}},
		"macOS blocks the program": {with(running("darwin"), func(f *Facts) {
			f.Program, f.Firewall.AppFirewall, f.Firewall.AppBlocked = "/opt/own git/owngit", true, true
		}),
			[]webui.Finding{{Code: webui.MsgDoctorMacBlocked, Args: []string{"/opt/own git/owngit"}, Repair: "sudo /usr/libexec/ApplicationFirewall/socketfilterfw --unblockapp '/opt/own git/owngit'"}}},
		"Windows allowed": {with(running("windows"), func(f *Facts) {
			f.Firewall.Rule, f.Firewall.Access = RuleAllows, service.FirewallAccess{Active: 2, On: 7, Allowed: 2}
		}), nil},
		"Windows allowed by another rule": {with(running("windows"), func(f *Facts) { f.Firewall.Access = service.FirewallAccess{Active: 2, On: 7, Allowed: 7} }), nil},
		"Windows Firewall off":            {with(running("windows"), func(f *Facts) { f.Firewall.Access = service.FirewallAccess{Active: 2, On: 4} }), nil},
		"Windows administrator without a rule": {with(running("windows"), func(f *Facts) { f.Administrator, f.Firewall.Access = true, private }),
			[]webui.Finding{{Code: webui.MsgDoctorWindowsRule, Args: []string{`C:\Program Files\OwnGit\owngit.exe`}, Repair: "owngit service install"}}},
		// Every path in the command is one literal PowerShell word.
		"Windows standard account with a rule for another program": {with(running("windows"), func(f *Facts) {
			f.Program, f.Firewall.Rule, f.Firewall.Access = "C:\\Users\\you\\a $(x) `b & [c] O\u2019k\\owngit.exe", RuleOther, private
		}), []webui.Finding{{Code: webui.MsgDoctorWindowsRuleAsk, Args: []string{"C:\\Users\\you\\a $(x) `b & [c] O\u2019k\\owngit.exe"},
			Repair: "& 'C:\\Windows\\System32\\netsh.exe' advfirewall firewall add rule 'name=OwnGit on private networks' dir=in action=allow profile=private " +
				"'program=C:\\Users\\you\\a $(x) `b & [c] O\u2019\u2019k\\owngit.exe'"}}},
		"Windows block rule": {with(running("windows"), func(f *Facts) {
			f.Firewall.Rule, f.Firewall.Access = RuleAllows, service.FirewallAccess{Active: 2, On: 7, Allowed: 2, Blocked: 2}
		}), []webui.Finding{{Code: webui.MsgDoctorWindowsBlocked, Args: []string{`C:\Program Files\OwnGit\owngit.exe`}}}},
		"Windows blocks all incoming connections": {with(running("windows"), func(f *Facts) {
			f.Firewall.Rule, f.Firewall.Access = RuleAllows, service.FirewallAccess{Active: 2, On: 7, BlockAll: 2, Allowed: 2}
		}), []webui.Finding{{Code: webui.MsgDoctorWindowsBlockAll}}},
		"Windows foreign rule": {with(running("windows"), func(f *Facts) { f.Administrator, f.Firewall.Rule, f.Firewall.Access = true, RuleForeign, private }),
			[]webui.Finding{{Code: webui.MsgDoctorWindowsForeignRule}}},
		"Windows public network": {with(running("windows"), func(f *Facts) {
			f.Firewall.Rule, f.Firewall.Access = RuleAllows, service.FirewallAccess{Active: 4, On: 7, Allowed: 2}
		}), []webui.Finding{{Code: webui.MsgDoctorWindowsPublic}}},
		// After the netsh repair a rule of another name allows private
		// networks: on a public network the fault is the network.
		"Windows public network with another rule": {with(running("windows"), func(f *Facts) {
			f.Firewall.Access = service.FirewallAccess{Active: 4, On: 7, Allowed: 2}
		}), []webui.Finding{{Code: webui.MsgDoctorWindowsPublic}}},
		"Windows domain network": {with(running("windows"), func(f *Facts) {
			f.Firewall.Rule, f.Firewall.Access = RuleAllows, service.FirewallAccess{Active: 1, On: 7, Allowed: 2}
		}), []webui.Finding{{Code: webui.MsgDoctorWindowsDomain}}},
		// Every account gets the same repair for a folder.
		"Windows folders": {with(running("windows"), func(f *Facts) {
			f.OtherDevices, f.AdministratorsFolders = false, []string{`C:\Users\you\AppData\Roaming\owngit`}
		}), []webui.Finding{{Code: webui.MsgDoctorAdministratorsFolder, Args: []string{`C:\Users\you\AppData\Roaming\owngit`}, Repair: "owngit service install"}}},
		"Windows folders of an administrator": {with(running("windows"), func(f *Facts) {
			f.OtherDevices, f.Administrator, f.AdministratorsFolders = false, true, []string{`D:\repos`}
		}), []webui.Finding{{Code: webui.MsgDoctorAdministratorsFolder, Args: []string{`D:\repos`}, Repair: "owngit service install"}}},
		"unchecked": {with(running("linux"), func(f *Facts) {
			f.Unchecked = []Unchecked{{Code: webui.MsgDoctorUncheckedFirewall, Reason: "permission denied"}}
		}), []webui.Finding{{Code: webui.MsgDoctorUncheckedFirewall, Args: []string{"permission denied"}, Unchecked: true}}},
	} {
		got := Diagnose(test.facts)
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, test.want)
		}
		for _, finding := range got {
			if !webui.Has(finding.Code) {
				t.Errorf("%s: %s has no sentence", name, finding.Code)
			}
			if strings.Contains(finding.Repair, "allow "+"7654") || strings.Contains(finding.Repair, "--add-port") {
				t.Errorf("%s: a port-wide rule %q", name, finding.Repair)
			}
		}
	}
}

// Only private networks of the addresses OwnGit listens on get a rule:
// never a public, shared (Tailscale) or link-local one.
func TestPrivateNetworks(t *testing.T) {
	interfaces := []Interface{
		{Name: "eth0", Zone: "home", Prefixes: []netip.Prefix{netip.MustParsePrefix("192.168.50.7/24"), netip.MustParsePrefix("fe80::1/64")}},
		{Name: "wan0", Zone: "public", Prefixes: []netip.Prefix{netip.MustParsePrefix("203.0.113.5/24")}},
		{Name: "tailscale0", Prefixes: []netip.Prefix{netip.MustParsePrefix("100.100.1.2/32")}},
		{Name: "eth1", Zone: "internal", Prefixes: []netip.Prefix{netip.MustParsePrefix("fd12:3456::5/64")}},
	}
	lan := Network{Prefix: netip.MustParsePrefix("192.168.50.0/24"), Zone: "home"}
	ula := Network{Prefix: netip.MustParsePrefix("fd12:3456::/64"), Zone: "internal"}
	for host, want := range map[string][]Network{
		"0.0.0.0":      {lan},
		"::":           {lan, ula},
		"":             {lan, ula},
		"192.168.50.7": {lan},
		"203.0.113.5":  nil,
		"100.100.1.2":  nil,
		"my-host":      nil,
	} {
		if got := PrivateNetworks(host, interfaces); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %+v, want %+v", host, got, want)
		}
	}
	// A private address whose prefix reaches past its private block names
	// public addresses too, so it is no private network; ordinary private
	// subnets of every block still are.
	for prefix, want := range map[string]string{
		"10.1.2.3/0":      "",
		"10.1.2.3/7":      "",
		"172.20.1.2/11":   "",
		"192.168.1.2/15":  "",
		"fd12:3456::1/1":  "",
		"fd12:3456::1/6":  "",
		"10.1.2.3/8":      "10.0.0.0/8",
		"172.20.1.2/12":   "172.16.0.0/12",
		"172.20.1.2/16":   "172.20.0.0/16",
		"192.168.1.2/24":  "192.168.1.0/24",
		"fd12:3456::1/7":  "fc00::/7",
		"fd12:3456::1/64": "fd12:3456::/64",
	} {
		got := PrivateNetworks("", []Interface{{Name: "eth0", Zone: "home", Prefixes: []netip.Prefix{netip.MustParsePrefix(prefix)}}})
		switch {
		case want == "" && len(got) != 0:
			t.Errorf("%s: %+v, want none", prefix, got)
		case want != "" && (len(got) != 1 || got[0].Prefix.String() != want):
			t.Errorf("%s: %+v, want %s", prefix, got, want)
		}
		if want == "" {
			if finding := ufwFinding(got, "7654"); finding.Repair != "" || finding.Code != webui.MsgDoctorUFWManual {
				t.Errorf("%s: %+v", prefix, finding)
			}
		}
	}
	// A computer on a public network only gets no command at all.
	public := PrivateNetworks("0.0.0.0", interfaces[1:3])
	finding := ufwFinding(public, "7654")
	if finding.Repair != "" || finding.Code != webui.MsgDoctorUFWManual {
		t.Errorf("public only: %+v", finding)
	}
}
