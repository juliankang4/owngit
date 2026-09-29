package doctor

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"owngit/internal/service"
	"owngit/internal/testfixture"
	"owngit/internal/webui"
)

// The netsh repair, pasted into PowerShell, adds a rule for exactly the
// program path, whatever PowerShell syntax the path holds, and runs none of
// it. It changes this computer's firewall, so it runs only with
// administrator rights and OWNGIT_TEST_FIREWALL=1, and removes its rule.
func TestNetshRepairIsLiteral(t *testing.T) {
	if os.Getenv("OWNGIT_TEST_FIREWALL") != "1" || !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("changes Windows Firewall: needs administrator rights and OWNGIT_TEST_FIREWALL=1")
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	netsh := filepath.Join(system, "netsh.exe")
	remove := func() {
		_ = exec.Command(netsh, "advfirewall", "firewall", "delete", "rule", "name=OwnGit on private networks").Run()
	}
	t.Cleanup(remove)
	testfixture.ForEachPowerShell(t, func(t *testing.T, shell string) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "MARK")
		for _, program := range []string{
			filepath.Join(dir, "plain", "owngit.exe"),
			filepath.Join(dir, "a b $(New-Item -ItemType File '"+marker+"') `x & [y] O\u2019k \u201cq\u201d", "owngit.exe"),
		} {
			facts := Facts{GOOS: "windows", Server: ServerRunning, SetupComplete: true, Listen: "0.0.0.0:7654", OtherDevices: true,
				Program: program, System: system, Firewall: Firewall{Access: service.FirewallAccess{Active: 2, On: 7}}}
			findings := Diagnose(facts)
			if len(findings) != 1 || findings[0].Code != webui.MsgDoctorWindowsRuleAsk {
				t.Fatalf("findings %+v", findings)
			}
			remove()
			if output, err := exec.Command(shell, "-NoProfile", "-NonInteractive", "-Command", findings[0].Repair).CombinedOutput(); err != nil {
				t.Fatalf("%s: %v\n%s", findings[0].Repair, err, output)
			}
			read := "[Console]::OutputEncoding = [Text.Encoding]::UTF8; (New-Object -ComObject HNetCfg.FwPolicy2).Rules | " +
				"Where-Object { $_.Name -eq 'OwnGit on private networks' } | ForEach-Object { $_.ApplicationName }"
			got, err := exec.Command(shell, "-NoProfile", "-NonInteractive", "-Command", read).Output()
			if err != nil {
				t.Fatal(err)
			}
			if got := string(bytes.TrimRight(got, "\r\n")); got != program {
				t.Errorf("rule program %q, want %q", got, program)
			}
			remove()
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("the command ran PowerShell code from the path (%v)", err)
		}
	})
}
