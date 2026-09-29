package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// On Windows a running program cannot be replaced: an archive update unpacks
// the new release into its own folder beside the current one.
func TestUpdateCommandOnWindows(t *testing.T) {
	archive := ClassifyExecutable(`C:\Users\you\Downloads\owngit_1.1.2_windows_amd64\owngit.exe`)
	npm := ClassifyExecutable(`C:\Users\you\AppData\Roaming\npm\node_modules\owngit\node_modules\owngit-win32-x64\bin\owngit.exe`)
	if archive.Route != RouteArchive || npm.Route != RouteNPM {
		t.Fatalf("routes %s and %s", archive.Route, npm.Route)
	}
	platform := Platform{GOOS: "windows", GOARCH: "amd64", Service: true}
	folder := `C:\Users\you\Downloads\owngit_1.1.3_windows_amd64`
	want := "Invoke-WebRequest -UseBasicParsing 'https://github.com/juliankang4/owngit/releases/download/v1.1.3/owngit_1.1.3_windows_amd64.zip' -OutFile '" + folder + ".zip'; " +
		"if ($?) { Expand-Archive '" + folder + ".zip' '" + folder + "'; if ($?) { & '" + folder + `\owngit.exe' service install } }`
	if got := archive.UpdateCommand("1.1.3", platform); got != want {
		t.Errorf("archive:\n got %s\nwant %s", got, want)
	}
	if got := archive.RemoveCommand("windows", false); got != `Remove-Item 'C:\Users\you\Downloads\owngit_1.1.2_windows_amd64\owngit.exe'` {
		t.Errorf("remove: %s", got)
	}
}

// Run in Windows PowerShell as printed, a failed download stops the command:
// nothing is unpacked and the last step never runs.
func TestWindowsCommandStopsAfterAFailedDownload(t *testing.T) {
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell is not available")
	}
	// The folder name has a typographic quote, which PowerShell also reads
	// as a single quote.
	dir := filepath.Join(t.TempDir(), "O\u2019Brien")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	install := Install{Route: RouteArchive, Executable: filepath.Join(dir, "owngit_1.1.2_windows_amd64", "owngit.exe")}
	command := install.UpdateCommand("1.1.3", Platform{GOOS: "windows", GOARCH: "amd64", Service: true})
	folder := filepath.Join(dir, "owngit_1.1.3_windows_amd64")
	marker := filepath.Join(dir, "last-step-ran")
	// A closed local port fails the download; a marker file stands in for
	// the service install.
	command = strings.Replace(command, "https://github.com/juliankang4/owngit/releases/download/v1.1.3/", "http://127.0.0.1:9/", 1)
	command = strings.Replace(command, "& "+powerShellQuote(filepath.Join(folder, "owngit.exe"))+" service install", "New-Item -ItemType File "+powerShellQuote(marker), 1)
	if !strings.Contains(command, "127.0.0.1:9") || !strings.Contains(command, "last-step-ran") {
		t.Fatalf("test command not rewritten: %s", command)
	}
	output, _ := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput()
	for _, path := range []string{folder, folder + ".zip", marker} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s exists after a failed download (%v)\n%s", path, err, output)
		}
	}
	// The same chain reaches the last step when every step succeeds.
	ok := powerShellChain([]string{"Write-Output first", "New-Item -ItemType File " + powerShellQuote(marker) + " | Out-Null"})
	if output, err := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-Command", ok).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("a successful chain did not reach its last step: %v", err)
	}
}
