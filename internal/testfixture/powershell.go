package testfixture

import (
	"os"
	"os/exec"
	"testing"
)

// ForEachPowerShell runs test once in Windows PowerShell 5.1 (powershell.exe)
// and once in PowerShell 7 (pwsh.exe), each as a subtest named after the
// executable, for commands that OwnGit prints or documents for "PowerShell".
// Windows always has powershell.exe. pwsh.exe is optional on a workstation,
// so its subtest is skipped when it is missing, except under CI (the CI
// variable is set), whose Windows runners have it.
func ForEachPowerShell(t *testing.T, test func(t *testing.T, shell string)) {
	t.Helper()
	for _, shell := range []string{"powershell.exe", "pwsh.exe"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				if shell == "pwsh.exe" && os.Getenv("CI") == "" {
					t.Skip("PowerShell 7 (pwsh.exe) is not on PATH; it is required only when CI is set")
				}
				t.Fatalf("%s is not on PATH: %v", shell, err)
			}
			test(t, shell)
		})
	}
}
