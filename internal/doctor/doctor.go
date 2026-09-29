// Package doctor finds what keeps an OwnGit installation from working on
// this computer and names one repair for each problem. "owngit doctor" and
// the Settings page show the same findings from the same facts.
//
// A finding names only what OwnGit read on this computer: whether the
// server answers, its setup, the owner of its folders, and the firewall
// configuration of this computer that it knows. It never claims that
// another device cannot connect, which OwnGit cannot see from here. A
// check that could not run is a finding too, never a clean result.
package doctor

import (
	"net"

	"owngit/internal/service"
	"owngit/internal/webui"
)

// Facts is what the checks read about one installation.
type Facts struct {
	// GOOS is the operating system, as runtime.GOOS names it.
	GOOS string
	// Running is true when the server answers its health check.
	Running bool
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
	// is an administrator. Its "owngit service install" gives the folders
	// back and adds the firewall rule with one approval.
	Administrator bool
	// AccountSID is the Windows account that runs OwnGit.
	AccountSID string
	// AdministratorsFolders are the state and repository folders that the
	// Administrators group owns on Windows.
	AdministratorsFolders []string
	// Firewall is the firewall configuration of this computer.
	Firewall Firewall
	// Unchecked names each check that could not run, with why.
	Unchecked []Unchecked
}

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
	// Linux: ufw is enabled, or firewalld runs.
	UFW       bool
	Firewalld bool
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

const privateProfile = 2

// Diagnose returns the findings for facts, most basic first. A server that
// does not answer is the only finding, since the other checks describe a
// running server.
func Diagnose(facts Facts) []webui.Finding {
	if !facts.Running {
		repair := "owngit service install"
		if facts.Service {
			repair = "owngit service start"
		}
		return []webui.Finding{{Code: webui.MsgDoctorNotRunning, Repair: repair}}
	}
	var findings []webui.Finding
	if !facts.SetupComplete {
		findings = append(findings, webui.Finding{Code: webui.MsgDoctorSetup, Repair: "owngit setup-link"})
	}
	for _, folder := range facts.AdministratorsFolders {
		finding := webui.Finding{Code: webui.MsgDoctorAdministratorsFolder, Args: []string{folder}}
		if facts.Administrator {
			finding.Repair = "owngit service install"
		} else {
			finding.Code = webui.MsgDoctorAdministratorsFolderAsk
			finding.Repair = `icacls "` + folder + `" /setowner "*` + facts.AccountSID + `" /T /C`
		}
		findings = append(findings, finding)
	}
	if facts.OtherDevices {
		findings = append(findings, firewallFindings(facts)...)
	}
	for _, unchecked := range facts.Unchecked {
		findings = append(findings, webui.Finding{Code: unchecked.Code, Args: []string{unchecked.Reason}})
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
		closed := firewall.Access.Closed()
		switch {
		case closed == 0:
		case closed&firewall.Access.Blocked != 0:
			return []webui.Finding{{Code: webui.MsgDoctorWindowsBlocked, Args: []string{facts.Program}}}
		case firewall.Rule == RuleForeign:
			return []webui.Finding{{Code: webui.MsgDoctorWindowsForeignRule}}
		case firewall.Rule == RuleAllows && closed&privateProfile == 0:
			return []webui.Finding{{Code: webui.MsgDoctorWindowsPublic}}
		case facts.Administrator:
			return []webui.Finding{{Code: webui.MsgDoctorWindowsRule, Args: []string{facts.Program}, Repair: "owngit service install"}}
		default:
			return []webui.Finding{{Code: webui.MsgDoctorWindowsRuleAsk, Args: []string{facts.Program},
				Repair: `netsh advfirewall firewall add rule name="OwnGit on private networks" dir=in action=allow profile=private program="` + facts.Program + `"`}}
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
			findings = append(findings, webui.Finding{Code: webui.MsgDoctorUFW, Args: []string{port}, Repair: "sudo ufw allow " + port + "/tcp"})
		}
		if firewall.Firewalld {
			findings = append(findings, webui.Finding{Code: webui.MsgDoctorFirewalld, Args: []string{port},
				Repair: "sudo firewall-cmd --permanent --add-port=" + port + "/tcp && sudo firewall-cmd --reload"})
		}
		return findings
	}
	return nil
}
