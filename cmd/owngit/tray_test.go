package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/server"
	"owngit/internal/service"
	"owngit/internal/state"
)

type trayReport struct {
	Available  bool   `json:"available"`
	Shown      bool   `json:"shown"`
	Desktop    bool   `json:"desktop"`
	StateDir   string `json:"state_dir"`
	AccessFile string `json:"access_file"`
}

func trayJSON(t *testing.T, arguments ...string) trayReport {
	t.Helper()
	output, err := captureStdout(func() error { return runCommand("tray", append(arguments, "--json")) })
	if err != nil {
		t.Fatalf("tray %v: %v", arguments, err)
	}
	var report trayReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("tray %v printed %q: %v", arguments, output, err)
	}
	return report
}

// The icon shows by default on a desktop, "off" hides it until "on", and
// the choice outlasts the command; a computer without a desktop says so.
func TestTrayCommand(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	original := probeEnvironment
	t.Cleanup(func() { probeEnvironment = original })
	probeEnvironment = func() service.Environment {
		return service.Environment{Getenv: func(string) string { return "" }, EUID: 1000, Linux: true, GraphicalSession: true}
	}

	report := trayJSON(t, "--state-dir", stateDir)
	if !report.Available || !report.Shown || !report.Desktop || report.StateDir != stateDir || report.AccessFile != filepath.Join(stateDir, state.TrayAccessFile) {
		t.Fatalf("default on a desktop: %+v", report)
	}
	if report := trayJSON(t, "off", "--state-dir", stateDir); report.Shown {
		t.Fatalf("off: %+v", report)
	}
	if report := trayJSON(t, "status", "--state-dir", stateDir); report.Shown {
		t.Fatalf("after off: %+v", report)
	}
	output, err := captureStdout(func() error { return runCommand("tray", []string{"--state-dir", stateDir}) })
	if err != nil || !strings.Contains(output, "hidden") || !strings.Contains(output, "owngit tray on") {
		t.Fatalf("hidden status printed %q err=%v", output, err)
	}
	if report := trayJSON(t, "on", "--state-dir", stateDir); !report.Shown {
		t.Fatalf("on: %+v", report)
	}

	probeEnvironment = func() service.Environment {
		return service.Environment{Getenv: func(string) string { return "" }, EUID: 1000, Linux: true}
	}
	if report := trayJSON(t, "--state-dir", stateDir); !report.Shown || report.Desktop {
		t.Fatalf("without a desktop: %+v", report)
	}
	output, err = captureStdout(func() error { return runCommand("tray", []string{"--state-dir", stateDir}) })
	if err != nil || !strings.Contains(output, "no desktop session") {
		t.Fatalf("status without a desktop printed %q err=%v", output, err)
	}

	output, err = captureStdout(func() error { return runCommand("tray", []string{"hide", "--state-dir", stateDir, "--json"}) })
	if err == nil || !strings.Contains(err.Error(), "tray takes on, off, status or nothing") {
		t.Fatalf("unknown operation: %q err=%v", output, err)
	}
}

// On Linux the state of the service that runs as the dedicated owngit
// account offers no icon: status says so in JSON, and on and off refuse
// with the reason, before anything reads that state.
func TestTrayCommandOnTheDedicatedAccountInstall(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the dedicated service account is a Linux install")
	}
	report := trayJSON(t, "--state-dir", service.AccountStateDir)
	if report.Available || report.Shown || report.Desktop || report.StateDir != service.AccountStateDir || report.AccessFile != "" {
		t.Fatalf("status of the dedicated account install: %+v", report)
	}
	output, err := captureStdout(func() error {
		return runCommand("tray", []string{"on", "--state-dir", service.AccountStateDir, "--json"})
	})
	if err == nil || !strings.Contains(err.Error(), "own service account") {
		t.Fatalf("tray on printed %q err=%v", output, err)
	}
}

// A running serve publishes the tray access file, private to this account,
// with its own address, and answers the tray status with its token; each
// start makes a new token and the old one is refused.
func TestServePublishesTrayAccess(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	read := func() state.TrayAccess {
		t.Helper()
		content, err := os.ReadFile(filepath.Join(stateDir, state.TrayAccessFile))
		noErr(t, err)
		var access state.TrayAccess
		noErr(t, json.Unmarshal(content, &access))
		return access
	}
	served := startServed(t, stateDir)
	access := read()
	if access.URL != served.url || access.Token == "" {
		t.Fatalf("access file %+v for a server at %s", access, served.url)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(stateDir, state.TrayAccessFile))
		noErr(t, err)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("access file mode %v", info.Mode().Perm())
		}
	}
	status := func(token string) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, access.URL+server.TrayStatusPath, nil)
		noErr(t, err)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		noErr(t, err)
		defer response.Body.Close()
		var answer server.TrayStatus
		if response.StatusCode == http.StatusOK {
			noErr(t, json.NewDecoder(response.Body).Decode(&answer))
			if answer.State != "attention" || !answer.SetupRequired {
				t.Fatalf("status before setup: %+v", answer)
			}
		}
		return response.StatusCode
	}
	if code := status(access.Token); code != http.StatusOK {
		t.Fatalf("status with the token: %d", code)
	}
	if code := status("not-the-token"); code != http.StatusUnauthorized {
		t.Fatalf("status with another token: %d", code)
	}
	served.stop()
	again := startServed(t, stateDir)
	next := read()
	if next.Token == access.Token || next.URL != again.url {
		t.Fatalf("after a restart the access file holds %+v, before %+v", next, access)
	}
	old := access.Token
	access = next
	if code := status(next.Token); code != http.StatusOK {
		t.Fatalf("status with the new token: %d", code)
	}
	if code := status(old); code != http.StatusUnauthorized {
		t.Fatalf("status with the token of the previous start: %d", code)
	}
}

// "owngit tray" runs as other state commands do on Windows: from a process
// with administrator rights it runs again without them, so tray-hidden
// belongs to the account. On Linux it never switches to the service
// account whose state the pointer names, and answers that this install has
// no icon; another state command still switches.
func TestTrayCommandRouting(t *testing.T) {
	previousProbe, previousRun := probeEnvironment, runWithoutAdminRights
	t.Cleanup(func() { probeEnvironment, runWithoutAdminRights = previousProbe, previousRun })
	probeEnvironment = func() service.Environment {
		return service.Environment{Getenv: func(string) string { return "" }, Windows: true, Administrator: true, Elevated: true}
	}
	var copies [][]string
	runWithoutAdminRights = func(arguments []string) (int, error) {
		copies = append(copies, arguments)
		return 0, nil
	}
	noErr(t, run([]string{"tray", "off", "--state-dir", "state"}))
	if want := [][]string{{"tray", "off", "--state-dir", "state"}}; !reflect.DeepEqual(copies, want) {
		t.Fatalf("elevated tray ran as %q, want a copy without administrator rights %q", copies, want)
	}
	probeEnvironment = previousProbe

	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("the service account pointer applies on Linux, to an account other than root")
	}
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "account-state")
	noErr(t, os.Mkdir(stateDir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })
	previousFile, previousApplies := pointerFile, pointerApplies
	t.Cleanup(func() { pointerFile, pointerApplies = previousFile, previousApplies })
	pointerFile, pointerApplies = filepath.Join(dir, "state-dir"), func() bool { return true }
	noErr(t, os.WriteFile(pointerFile, []byte(stateDir+"\n"), 0o644))
	output, err := captureStdout(func() error { return run([]string{"tray", "status", "--json"}) })
	var report trayReport
	if err != nil || json.Unmarshal([]byte(output), &report) != nil || report.Available || report.StateDir != stateDir {
		t.Fatalf("tray status on the service account install printed %q err=%v", output, err)
	}
	if err := run([]string{"upgrade-backup", "--state-dir", stateDir}); err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("another state command did not switch to the service account: %v", err)
	}
}

// A tray access file that cannot be replaced leaves the server running and
// says why in the log; the status then answers no one.
func TestServeLogsAnUnwrittenTrayAccessFile(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	noErr(t, os.MkdirAll(filepath.Join(stateDir, state.TrayAccessFile), 0o700))
	served := startServed(t, stateDir)
	if log := served.log(); !strings.Contains(log, "the tray access file could not be written: write the tray access file:") {
		t.Fatalf("serve did not log the failure:\n%s", log)
	}
	response, err := http.Get(served.url + server.TrayStatusPath)
	noErr(t, err)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status without a published token: %d", response.StatusCode)
	}
}
