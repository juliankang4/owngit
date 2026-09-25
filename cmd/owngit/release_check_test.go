package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"owngit/internal/state"
)

func TestMain(m *testing.M) {
	// No test may contact GitHub. A server started by any test in this
	// package points its release check at a closed local port unless the
	// test sets its own endpoint.
	releaseCheckEndpoint = "http://127.0.0.1:0/owngit-tests-never-contact-github"
	// A test run from a terminal must not ask setup questions there.
	interactiveSetup = func() bool { return false }
	os.Exit(m.Run())
}

// fakeReleaseEndpoint counts requests and answers with a newer release. It
// also sets a short initial delay for the servers the test starts.
func fakeReleaseEndpoint(t *testing.T) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = writer.Write([]byte(`{"tag_name":"v99.0.0","draft":false,"prerelease":false}`))
	}))
	previousURL, previousDelay := releaseCheckEndpoint, releaseCheckDelay
	releaseCheckEndpoint, releaseCheckDelay = endpoint.URL, 10*time.Millisecond
	t.Cleanup(func() {
		endpoint.Close()
		releaseCheckEndpoint, releaseCheckDelay = previousURL, previousDelay
	})
	return &hits
}

func initializedState(t *testing.T, updateCheck bool) string {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	repositories := filepath.Join(root, "repositories")
	noErr(t, os.Mkdir(repositories, 0o700))
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	noErr(t, store.CompleteSetup(ctx, repositories, "open", "", "synthetic-admin-hash", true))
	noErr(t, store.SetUpdateCheck(ctx, updateCheck))
	noErr(t, store.Close())
	return stateDir
}

// The check runs after startup when it is on, and makes no request when
// --no-update-check is given, even though the saved setting is on.
func TestReleaseCheckRunsUnlessTheStartOptionForbidsIt(t *testing.T) {
	hits := fakeReleaseEndpoint(t)

	instance := startServed(t, initializedState(t, true))
	deadline := time.Now().Add(10 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	instance.stop()
	if hits.Load() == 0 {
		t.Fatalf("an enabled check never asked for the latest release\n%s", instance.log())
	}

	hits.Store(0)
	instance = startServed(t, initializedState(t, true), "--no-update-check")
	time.Sleep(500 * time.Millisecond)
	instance.stop()
	if got := hits.Load(); got != 0 {
		t.Fatalf("--no-update-check still made %d requests", got)
	}
}

func TestReleaseCheckHonoursTheSavedSettingAtStartup(t *testing.T) {
	hits := fakeReleaseEndpoint(t)
	instance := startServed(t, initializedState(t, false))
	time.Sleep(500 * time.Millisecond)
	instance.stop()
	if got := hits.Load(); got != 0 {
		t.Fatalf("a check turned off in Settings made %d requests", got)
	}
}

func TestReleaseCheckWaitsForSetup(t *testing.T) {
	hits := fakeReleaseEndpoint(t)
	instance := startServed(t, filepath.Join(t.TempDir(), "state"))
	time.Sleep(500 * time.Millisecond)
	instance.stop()
	if got := hits.Load(); got != 0 {
		t.Fatalf("an installation before setup made %d requests", got)
	}
}
