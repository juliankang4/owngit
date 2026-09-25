package main

import (
	"context"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/auth"
	"owngit/internal/state"
	"owngit/internal/webui"
)

// The Settings page of a real serve process shows what that process uses,
// because it holds the running-record lock, and reports a saved change as
// waiting for a restart, like "owngit network show".
func TestSettingsPageShowsWhatTheServeProcessUses(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	repositoryRoot := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositoryRoot, 0o700))
	canonical, err := filepath.EvalSymlinks(repositoryRoot)
	noErr(t, err)
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	adminHash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, canonical, "open", "", adminHash, true))
	noErr(t, store.Close())
	address := freeLoopbackAddress(t)
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--listen", address)
	noErr(t, err)

	instance := startServedWith(t, []string{"--state-dir", stateDir, "--no-open"})
	settings := func() string {
		response, err := http.Get(instance.url + "/settings?lang=en")
		noErr(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		noErr(t, err)
		return string(body)
	}
	text := func(code webui.MessageCode) string { return html.EscapeString(webui.Text(webui.LangEN, code)) }
	before := settings()
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--allowed-host", "later.test")
	noErr(t, err)
	after := settings()
	instance.stop()

	if !strings.Contains(before, text(webui.MsgNetCurrent)) || strings.Count(before, address) < 2 {
		t.Fatal("the Settings page of a running server did not show the values it uses")
	}
	if !strings.Contains(after, text(webui.MsgNetRestart)) || !strings.Contains(after, "later.test") {
		t.Fatal("a name saved while running was not shown as waiting for a restart")
	}
}
