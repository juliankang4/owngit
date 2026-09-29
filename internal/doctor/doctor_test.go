package doctor

import (
	"reflect"
	"testing"

	"owngit/internal/service"
	"owngit/internal/webui"
)

// Each problem gets one finding with one repair, a firewall is looked at
// only when OwnGit listens for other devices, and a check that could not
// run is a finding, never a clean result.
func TestDiagnose(t *testing.T) {
	running := func(goos string) Facts {
		return Facts{GOOS: goos, Running: true, SetupComplete: true, Listen: "0.0.0.0:7654", OtherDevices: true, Program: `C:\Program Files\OwnGit\owngit.exe`, AccountSID: "S-1-5-21-1"}
	}
	with := func(facts Facts, change func(*Facts)) Facts {
		change(&facts)
		return facts
	}
	// Windows: a network in use (private) with the firewall on.
	private := service.FirewallAccess{Active: 2, On: 7}
	for name, test := range map[string]struct {
		facts Facts
		want  []webui.Finding
	}{
		"not running without a service": {Facts{GOOS: "linux"}, []webui.Finding{{Code: webui.MsgDoctorNotRunning, Repair: "owngit service install"}}},
		"not running with a service": {Facts{GOOS: "linux", Service: true, AdministratorsFolders: []string{"x"}},
			[]webui.Finding{{Code: webui.MsgDoctorNotRunning, Repair: "owngit service start"}}},
		"setup": {with(running("linux"), func(f *Facts) { f.SetupComplete, f.OtherDevices = false, false }),
			[]webui.Finding{{Code: webui.MsgDoctorSetup, Repair: "owngit setup-link"}}},
		"all well": {running("linux"), nil},
		"this computer only, whatever the firewall": {with(running("linux"), func(f *Facts) { f.OtherDevices, f.Firewall.UFW = false, true }), nil},
		"ufw and firewalld": {with(running("linux"), func(f *Facts) { f.Firewall.UFW, f.Firewall.Firewalld = true, true }), []webui.Finding{
			{Code: webui.MsgDoctorUFW, Args: []string{"7654"}, Repair: "sudo ufw allow 7654/tcp"},
			{Code: webui.MsgDoctorFirewalld, Args: []string{"7654"}, Repair: "sudo firewall-cmd --permanent --add-port=7654/tcp && sudo firewall-cmd --reload"},
		}},
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
		"Windows standard account with a rule for another program": {with(running("windows"), func(f *Facts) {
			f.Program, f.Firewall.Rule, f.Firewall.Access = `C:\Users\you\owngit.exe`, RuleOther, private
		}), []webui.Finding{{Code: webui.MsgDoctorWindowsRuleAsk, Args: []string{`C:\Users\you\owngit.exe`},
			Repair: `netsh advfirewall firewall add rule name="OwnGit on private networks" dir=in action=allow profile=private program="C:\Users\you\owngit.exe"`}}},
		"Windows block rule": {with(running("windows"), func(f *Facts) {
			f.Firewall.Rule, f.Firewall.Access = RuleAllows, service.FirewallAccess{Active: 2, On: 7, Allowed: 2, Blocked: 2}
		}), []webui.Finding{{Code: webui.MsgDoctorWindowsBlocked, Args: []string{`C:\Program Files\OwnGit\owngit.exe`}}}},
		"Windows foreign rule": {with(running("windows"), func(f *Facts) { f.Administrator, f.Firewall.Rule, f.Firewall.Access = true, RuleForeign, private }),
			[]webui.Finding{{Code: webui.MsgDoctorWindowsForeignRule}}},
		"Windows public network": {with(running("windows"), func(f *Facts) {
			f.Firewall.Rule, f.Firewall.Access = RuleAllows, service.FirewallAccess{Active: 4, On: 7, Allowed: 2}
		}),
			[]webui.Finding{{Code: webui.MsgDoctorWindowsPublic}}},
		"Windows folders": {with(running("windows"), func(f *Facts) {
			f.OtherDevices, f.AdministratorsFolders = false, []string{`C:\Users\you\AppData\Roaming\owngit`}
		}), []webui.Finding{{Code: webui.MsgDoctorAdministratorsFolderAsk, Args: []string{`C:\Users\you\AppData\Roaming\owngit`},
			Repair: `icacls "C:\Users\you\AppData\Roaming\owngit" /setowner "*S-1-5-21-1" /T /C`}}},
		"Windows folders of an administrator": {with(running("windows"), func(f *Facts) {
			f.OtherDevices, f.Administrator, f.AdministratorsFolders = false, true, []string{`D:\repos`}
		}), []webui.Finding{{Code: webui.MsgDoctorAdministratorsFolder, Args: []string{`D:\repos`}, Repair: "owngit service install"}}},
		"unchecked": {with(running("linux"), func(f *Facts) {
			f.Unchecked = []Unchecked{{Code: webui.MsgDoctorUncheckedFirewall, Reason: "permission denied"}}
		}), []webui.Finding{{Code: webui.MsgDoctorUncheckedFirewall, Args: []string{"permission denied"}}}},
	} {
		got := Diagnose(test.facts)
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, test.want)
		}
		for _, finding := range got {
			if !webui.Has(finding.Code) {
				t.Errorf("%s: %s has no sentence", name, finding.Code)
			}
		}
	}
}
