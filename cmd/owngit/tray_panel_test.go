package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/tray"
)

func trayRead(t *testing.T, stateDir string) (bool, tray.Panel) {
	t.Helper()
	output, err := captureStdout(func() error {
		return runCommand("tray", []string{"read", "--json", "--lang", "ko", "--state-dir", stateDir})
	})
	if err != nil {
		t.Fatalf("tray read: %v\n%s", err, output)
	}
	var answer struct {
		Shown bool       `json:"shown"`
		Panel tray.Panel `json:"panel"`
	}
	if err := json.Unmarshal([]byte(output), &answer); err != nil {
		t.Fatalf("tray read printed %q: %v", output, err)
	}
	return answer.Shown, answer.Panel
}

// "owngit tray read" gives a panel drawn by another program what the icon
// shows, and "owngit tray open" opens the dashboard only after the server
// proves again that it answers: a program that takes OwnGit's address after
// it stops is shown as Status unavailable and never gets the browser.
func TestTrayReadAndOpen(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	served := startServed(t, stateDir)
	shown, panel := trayRead(t, stateDir)
	if !shown || panel.Condition != "attention" || panel.State != "확인 필요" || !panel.CanOpen || panel.CloneAddress != served.url+"/git/" {
		t.Fatalf("panel of a running server: shown=%v %+v", shown, panel)
	}
	if runtime.GOOS != "linux" {
		t.Skip("the browser opens through xdg-open on Linux")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// An opener that ends with a failure, as when no browser takes the
	// address, is open_failed, not an opened dashboard.
	noErr(t, os.WriteFile(filepath.Join(bin, "xdg-open"), []byte("#!/bin/sh\nexit 3\n"), 0o755))
	err := runCommand("tray", []string{"open", "--json", "--state-dir", stateDir})
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != "open_failed" || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("tray open with a failing opener: %v", err)
	}
	opened := filepath.Join(bin, "opened")
	noErr(t, os.WriteFile(filepath.Join(bin, "xdg-open"), []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> "+opened+"\n"), 0o755))
	output, err := captureStdout(func() error { return runCommand("tray", []string{"open", "--json", "--state-dir", stateDir}) })
	var result struct {
		OK  bool   `json:"ok"`
		URL string `json:"url"`
	}
	if err != nil || json.Unmarshal([]byte(output), &result) != nil || !result.OK || result.URL != served.url {
		t.Fatalf("tray open printed %q: %v", output, err)
	}
	// The browser starts in the background.
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(20 * time.Millisecond) {
		if content, _ := os.ReadFile(opened); string(content) == served.url+"\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the dashboard was not opened")
		}
	}

	served.stop()
	address, err := url.Parse(served.url)
	noErr(t, err)
	listener, err := net.Listen("tcp", address.Host)
	noErr(t, err)
	imposter := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-OwnGit-Tray-Proof", "forged")
		_, _ = writer.Write([]byte(`{"ok":true,"state":"running","version":"9","shown":true,"dashboard_url":"http://elsewhere.invalid","clone_address":"http://elsewhere.invalid/git/","setup_required":false,"update":null,"findings":[],"pushes":[]}`))
	})}
	go func() { _ = imposter.Serve(listener) }()
	t.Cleanup(func() { _ = imposter.Close() })

	if err := runCommand("tray", []string{"open", "--json", "--state-dir", stateDir}); err == nil || !strings.Contains(err.Error(), "did not prove") {
		t.Fatalf("tray open with another program at the address: %v", err)
	}
	if _, panel := trayRead(t, stateDir); panel.Condition != "unavailable" || panel.CanOpen || panel.CloneAddress != "" {
		t.Fatalf("panel of another program at the address: %+v", panel)
	}
	if content, _ := os.ReadFile(opened); string(content) != served.url+"\n" {
		t.Fatalf("the browser was sent to another program: %q", content)
	}
}
