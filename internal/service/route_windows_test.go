package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/testfixture"
)

// On Windows a running program cannot be replaced: the installer unpacks the
// new release into its own folder beside the current one.
func TestUpdateCommandOnWindows(t *testing.T) {
	archive := ClassifyExecutable(`C:\Users\you\Downloads\owngit_1.1.2_windows_amd64\owngit.exe`)
	npm := ClassifyExecutable(`C:\Users\you\AppData\Roaming\npm\node_modules\owngit\node_modules\owngit-win32-x64\bin\owngit.exe`)
	if archive.Route != RouteArchive || npm.Route != RouteNPM {
		t.Fatalf("routes %s and %s", archive.Route, npm.Route)
	}
	platform := Platform{GOOS: "windows", GOARCH: "amd64", Service: true}
	want := "& ([scriptblock]::Create((irm -MaximumRedirection 0 'https://raw.githubusercontent.com/juliankang4/owngit/v1.1.3/packaging/installer/install.ps1'))) -Version 1.1.3 -Dir 'C:\\Users\\you\\Downloads'"
	if got := archive.UpdateCommand("1.1.3", platform); got != want {
		t.Errorf("archive:\n got %s\nwant %s", got, want)
	}
	platform.Service = false
	if got := archive.UpdateCommand("1.1.3", platform); got != want+" -NoService" {
		t.Errorf("archive without a service:\n got %s", got)
	}
	if got := archive.RemoveCommand("windows", false); got != `Remove-Item -LiteralPath 'C:\Users\you\Downloads\owngit_1.1.2_windows_amd64\owngit.exe'` {
		t.Errorf("remove: %s", got)
	}
}

// Run in Windows PowerShell, a chained command stops at a failed step and
// reaches its last step when every step succeeds.
func TestPowerShellChainRunsInWindowsPowerShell(t *testing.T) {
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell is not available")
	}
	marker := filepath.Join(t.TempDir(), "last-step-ran")
	last := "New-Item -ItemType File -Path " + PowerShellQuote(marker) + " | Out-Null"
	failed := powerShellChain([]string{"Get-Item -LiteralPath " + PowerShellQuote(marker+".missing"), last})
	output, _ := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", failed).CombinedOutput()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the last step ran after a failed one (%v)\n%s", err, output)
	}
	ok := powerShellChain([]string{"Write-Output first", last})
	if output, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", ok).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("a successful chain did not reach its last step: %v", err)
	}
}

// A folder name can hold [ and ], which PowerShell parameters that accept
// wildcards read as a pattern, and a typographic quote, which PowerShell
// reads as a single quote. Run as printed, the update command hands the
// installer the folder literally (a stub stands in for install.ps1 and
// records its parameters; TestInstallPs1 in tools/release runs the real
// one in such a folder), and the remove command deletes this program, not
// the one in a folder that the pattern matches.
func TestWindowsCommandsTakeBracketedPathsLiterally(t *testing.T) {
	testfixture.ForEachPowerShell(t, testBracketedPaths)
}

func testBracketedPaths(t *testing.T, powershell string) {
	root := t.TempDir()
	dir := filepath.Join(root, "O\u2019Brien [x]")
	decoy := filepath.Join(root, "O\u2019Brien x")
	for _, folder := range []string{dir, decoy} {
		noErr(t, os.MkdirAll(filepath.Join(folder, "owngit_1.1.2_windows_amd64"), 0o700))
		noErr(t, os.WriteFile(filepath.Join(folder, "owngit_1.1.2_windows_amd64", "owngit.exe"), []byte("old"), 0o700))
	}
	record := filepath.Join(root, "parameters.txt")
	stub := "param([string]$Version, [switch]$NoService, [string]$Dir)\n" +
		"[IO.File]::WriteAllLines(" + PowerShellQuote(record) + ", [string[]]@($Version, $NoService.IsPresent, $Dir))\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.1.3/packaging/installer/install.ps1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(stub))
	}))
	defer server.Close()

	install := Install{Route: RouteArchive, Executable: filepath.Join(dir, "owngit_1.1.2_windows_amd64", "owngit.exe")}
	command := strings.Replace(install.UpdateCommand("1.1.3", Platform{GOOS: "windows", GOARCH: "amd64"}), installerSource, server.URL+"/v", 1)
	if output, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", command, err, output)
	}
	recorded, err := os.ReadFile(record)
	noErr(t, err)
	if got, want := strings.TrimRight(string(recorded), "\r\n"), strings.Join([]string{"1.1.3", "True", dir}, "\r\n"); got != want {
		t.Errorf("the installer got %q, want %q", got, want)
	}

	remove := install.RemoveCommand("windows", false)
	if output, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", remove).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", remove, err, output)
	}
	if _, err := os.Stat(install.Executable); !os.IsNotExist(err) {
		t.Errorf("%s stayed: %v", install.Executable, err)
	}
	if _, err := os.Stat(filepath.Join(decoy, "owngit_1.1.2_windows_amd64", "owngit.exe")); err != nil {
		t.Errorf("the remove command deleted the program in %s: %v", decoy, err)
	}
}
