package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A start whose input and output are not a terminal, such as brew services,
// a LaunchAgent, systemd or CI, keeps the private setup file flow.
func TestNonTerminalStartKeepsTheSetupFile(t *testing.T) {
	previous := interactiveSetup
	t.Cleanup(func() { interactiveSetup = previous })
	interactiveSetup = defaultInteractiveSetup
	reader, writer, err := os.Pipe()
	noErr(t, err)
	defer reader.Close()
	defer writer.Close()
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = reader, writer
	t.Cleanup(func() { os.Stdin, os.Stdout = stdin, stdout })

	stateDir := filepath.Join(t.TempDir(), "state")
	instance := startServed(t, stateDir)
	defer instance.stop()
	if !strings.Contains(instance.log(), "owner setup file: "+filepath.Join(stateDir, "owner-setup.html")) {
		t.Fatalf("no setup file was issued:\n%s", instance.log())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "owner-setup.html")); err != nil {
		t.Fatal(err)
	}
	requireSetupFilePage(t, instance.url)
}

// When the terminal flow was chosen but the terminal cannot be used after
// all, setup falls back to the setup file, and the browser gets the same
// page as in a start without a terminal, not an approval request that the
// missing terminal could never answer.
func TestUnusableTerminalFallsBackToTheSetupFilePage(t *testing.T) {
	previous := interactiveSetup
	t.Cleanup(func() { interactiveSetup = previous })
	interactiveSetup = func() bool { return true }
	reader, writer, err := os.Pipe()
	noErr(t, err)
	defer reader.Close()
	defer writer.Close()
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = reader, writer
	t.Cleanup(func() { os.Stdin, os.Stdout = stdin, stdout })

	stateDir := filepath.Join(t.TempDir(), "state")
	instance := startServed(t, stateDir)
	defer instance.stop()
	path := filepath.Join(stateDir, "owner-setup.html")
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(instance.log(), "owner setup file: "+path) {
		if time.Now().After(deadline) {
			t.Fatalf("no setup file was issued:\n%s", instance.log())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(instance.log(), "terminal setup unavailable") {
		t.Fatalf("the fallback was not logged:\n%s", instance.log())
	}
	// The server test TestAbandonedApprovalServesTheSetupFileFlow covers
	// the refused approval request.
	requireSetupFilePage(t, instance.url)
}

// requireSetupFilePage checks that /setup offers only the setup file flow.
func requireSetupFilePage(t *testing.T, base string) {
	t.Helper()
	response, err := http.Get(base + "/setup")
	noErr(t, err)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(body), "data-redeem-form") || strings.Contains(string(body), "data-redeem-optional") ||
		strings.Contains(string(body), "/setup/approval") {
		t.Fatal("the setup page does not offer the setup file flow")
	}
}
