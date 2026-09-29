package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The firewall reading parses what this Windows computer prints. It only
// reads the firewall; run with -v to see the lines.
func TestFirewallAccessScriptOnThisComputer(t *testing.T) {
	powershell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(powershell); err != nil {
		t.Skip("Windows PowerShell is not available")
	}
	command := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", FirewallAccessScript)
	command.Env = append(os.Environ(), FirewallProgramVariable+`=C:\Program Files\OwnGit\owngit.exe`)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	t.Logf("FirewallAccessScript printed:\n%q", output)
	access, err := ParseFirewallAccess(string(output), "7654")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("port 7654: %+v, closed %d", access, access.Closed())
}
