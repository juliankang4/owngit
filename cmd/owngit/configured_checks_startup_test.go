package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

func TestUnavailableCheckWorkspacePreservesDataAndDoesNotBlockServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	noErr(t, store.Close())
	unknown := filepath.Join(stateDir, "runtime", "check-jobs", strings.Repeat("a", 32))
	noErr(t, os.MkdirAll(unknown, 0o700))
	sentinel := filepath.Join(unknown, "unrelated.txt")
	noErr(t, os.WriteFile(sentinel, []byte("preserve"), 0o600))
	served := startServed(t, stateDir)
	if !strings.Contains(served.log(), "configured check runtime unavailable") {
		t.Fatalf("check runtime unavailability was not logged:\n%s", served.log())
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Get(served.url + "/")
	noErr(t, err)
	response.Body.Close()
	served.stop()
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "preserve" {
		t.Fatalf("sentinel content=%q err=%v", content, err)
	}
}

func TestServeStartsAndStopsWithConfiguredCheckCoordinator(t *testing.T) {
	served := startServed(t, filepath.Join(t.TempDir(), "state"))
	response, err := (&http.Client{Timeout: 2 * time.Second}).Get(served.url + "/")
	noErrf(t, err, "request started serve")
	response.Body.Close()
	served.stop()
}
