package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/server"
	"owngit/internal/service"
	"owngit/internal/state"
)

type trayReport struct {
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
	if !report.Shown || !report.Desktop || report.AccessFile != filepath.Join(report.StateDir, state.TrayAccessFile) {
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

// A running serve publishes the tray access file, private to this account,
// with its own address, and answers the tray status with its token; the
// token outlasts a restart.
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
	if next := read(); next.Token != access.Token || next.URL != again.url {
		t.Fatalf("after a restart the access file holds %+v, before %+v", next, access)
	}
}
