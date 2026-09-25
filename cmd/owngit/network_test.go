package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

func TestEffectiveServeNetworkPrecedence(t *testing.T) {
	parse := func(arguments ...string) (*flag.FlagSet, *string, *string) {
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		listen := flags.String("listen", "127.0.0.1:7654", "")
		baseURL := flags.String("base-url", "", "")
		noErr(t, flags.Parse(arguments))
		return flags, listen, baseURL
	}
	saved := state.NetworkSettings{Listen: "0.0.0.0:7700", BaseURL: "HTTP://gitbox.internal:7700"}
	cases := []struct {
		name      string
		saved     state.NetworkSettings
		arguments []string
		want      serveNetwork
	}{
		{"defaults", state.NetworkSettings{}, nil, serveNetwork{"127.0.0.1:7654", "default", "", "default"}},
		{"saved", saved, nil, serveNetwork{"0.0.0.0:7700", "saved", "http://gitbox.internal:7700", "saved"}},
		{"flags win", saved, []string{"--listen", "127.0.0.1:0", "--base-url", "http://other.internal:1"},
			serveNetwork{"127.0.0.1:0", "flag", "http://other.internal:1", "flag"}},
		{"flag equal to the default still wins", saved, []string{"--listen", "127.0.0.1:7654"},
			serveNetwork{"127.0.0.1:7654", "flag", "http://gitbox.internal:7700", "saved"}},
		{"empty base URL flag derives for this run", saved, []string{"--base-url", ""},
			serveNetwork{"0.0.0.0:7700", "saved", "", "flag"}},
	}
	for _, test := range cases {
		flags, listen, baseURL := parse(test.arguments...)
		got, err := effectiveServeNetwork(test.saved, flags, *listen, *baseURL)
		if err != nil || got != test.want {
			t.Errorf("%s: got %+v err=%v, want %+v", test.name, got, err, test.want)
		}
	}
	for _, bad := range []state.NetworkSettings{{Listen: "nonsense"}, {BaseURL: "http://gitbox.internal/path"}} {
		flags, listen, baseURL := parse()
		if _, err := effectiveServeNetwork(bad, flags, *listen, *baseURL); err == nil || !strings.Contains(err.Error(), "owngit network reset") {
			t.Errorf("invalid saved %+v: err=%v, want a pointer to network reset", bad, err)
		}
		// A flag replaces a bad saved value, so the owner can still start.
		flags, listen, baseURL = parse("--listen", "127.0.0.1:0", "--base-url", "")
		if _, err := effectiveServeNetwork(bad, flags, *listen, *baseURL); err != nil {
			t.Errorf("flags did not override invalid saved %+v: %v", bad, err)
		}
	}
}

func networkJSON(t *testing.T, stateDir string) networkReport {
	t.Helper()
	output, err := captureStdout(func() error { return run([]string{"network", "show", "--state-dir", stateDir, "--json"}) })
	noErr(t, err)
	var report networkReport
	noErr(t, json.Unmarshal([]byte(output), &report))
	return report
}

func runNetwork(t *testing.T, arguments ...string) (string, error) {
	t.Helper()
	return captureStdout(func() error { return run(append([]string{"network"}, arguments...)) })
}

func TestNetworkSetShowAndReset(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	// set works before the first start and creates the state, like
	// approve-host.
	output, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "0.0.0.0:7720")
	noErr(t, err)
	if !strings.Contains(output, "apply at the next start") || !strings.Contains(output, "plain HTTP") || !strings.Contains(output, "--base-url or --allowed-host") {
		t.Fatalf("set output: %q", output)
	}
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	noErr(t, store.AddTrustedHost(context.Background(), "Legacy.Example"))
	noErr(t, store.Close())
	output, err = runNetwork(t, "set", "--state-dir", stateDir, "--base-url", "http://GitBox.internal:7720",
		"--allowed-host", "GitBox.Internal.", "--allowed-host", "100.64.0.7", "--remove-allowed-host", "legacy.example", "--remove-allowed-host", "never.example")
	noErr(t, err)
	if strings.Contains(output, "plain HTTP") || !strings.Contains(output, "never.example was not an allowed Host") {
		t.Fatalf("second set output: %q", output)
	}
	report := networkJSON(t, stateDir)
	if report.Saved.Listen != "0.0.0.0:7720" || report.Saved.BaseURL != "http://GitBox.internal:7720" ||
		!reflect.DeepEqual(report.Saved.AllowedHosts, []string{"100.64.0.7", "gitbox.internal"}) {
		t.Fatalf("saved=%+v", report.Saved)
	}
	if report.Server != "not_running" || report.Running != nil || report.RestartNeeded ||
		report.NextStart.Listen != "0.0.0.0:7720" || report.NextStart.ListenSource != "saved" || report.NextStart.BaseURLSource != "saved" {
		t.Fatalf("report=%+v", report)
	}
	text, err := runNetwork(t, "show", "--state-dir", stateDir)
	noErr(t, err)
	if !strings.Contains(text, "No OwnGit server is running") || !strings.Contains(text, "0.0.0.0:7720") {
		t.Fatalf("show text: %q", text)
	}

	// Something that holds the state directory without a record, such as an
	// older server or an offline backup, is reported as unknown.
	release, err := state.AcquireOfflineLock(stateDir)
	noErr(t, err)
	held := networkJSON(t, stateDir)
	release()
	if held.Server != "unknown" || held.Running != nil {
		t.Fatalf("held state directory report=%+v", held)
	}

	// Invalid values are refused and nothing is saved.
	for _, arguments := range [][]string{
		{"--listen", "0.0.0.0"}, {"--listen", "127.0.0.1:0"}, {"--base-url", "http://gitbox.internal/owngit"},
		{"--base-url", "http://user@gitbox.internal"}, {"--base-url", "http://gitbox.test:99999"}, {"--base-url", "http://gitbox.test:"},
		{"--allowed-host", "bad name"},
		{"--listen", "127.0.0.1:7721", "--allowed-host", "a.example", "--remove-allowed-host", "A.example"}, {},
	} {
		if _, err := captureStderr(func() error {
			_, err := runNetwork(t, append([]string{"set", "--state-dir", stateDir}, arguments...)...)
			return err
		}); err == nil {
			t.Errorf("set %v succeeded", arguments)
		}
	}
	if after := networkJSON(t, stateDir); !reflect.DeepEqual(after.Saved, report.Saved) {
		t.Fatalf("a refused set changed %+v to %+v", report.Saved, after.Saved)
	}

	// An empty value removes one saved setting.
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--base-url", "")
	noErr(t, err)
	if after := networkJSON(t, stateDir); after.Saved.BaseURL != "" || after.Saved.Listen != "0.0.0.0:7720" {
		t.Fatalf("after removing the base URL: %+v", after.Saved)
	}

	output, err = runNetwork(t, "reset", "--state-dir", stateDir)
	noErr(t, err)
	if !strings.Contains(output, "127.0.0.1:7654") || !strings.Contains(output, "Allowed Hosts kept: 100.64.0.7, gitbox.internal") {
		t.Fatalf("reset output: %q", output)
	}
	after := networkJSON(t, stateDir)
	if after.Saved.Listen != "" || after.Saved.BaseURL != "" || len(after.Saved.AllowedHosts) != 2 || after.NextStart.Listen != "127.0.0.1:7654" {
		t.Fatalf("after reset: %+v", after)
	}
	_, err = runNetwork(t, "reset", "--state-dir", stateDir, "--clear-allowed-hosts")
	noErr(t, err)
	if after := networkJSON(t, stateDir); len(after.Saved.AllowedHosts) != 0 {
		t.Fatalf("hosts after reset --clear-allowed-hosts: %v", after.Saved.AllowedHosts)
	}
	if _, err := runNetwork(t, "show", "--state-dir", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("show created or accepted a missing state directory")
	}
}

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	noErr(t, err)
	address := listener.Addr().String()
	noErr(t, listener.Close())
	return address
}

func statusForHost(t *testing.T, base, host string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, base+"/", nil)
	noErr(t, err)
	request.Host = host
	response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	noErr(t, err)
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response.StatusCode
}

// The saved listen address and base URL apply at start, a flag overrides
// them for one run without changing them, "network show" tells saved from
// running values, and loopback names stay accepted whatever is saved.
func TestServeAppliesSavedNetworkSettings(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	address := freeLoopbackAddress(t)
	_, port, _ := net.SplitHostPort(address)
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", address, "--base-url", "http://gitbox.test:"+port)
	noErr(t, err)

	instance := startServedWith(t, []string{"--state-dir", stateDir, "--no-open"})
	if instance.url != "http://"+address {
		instance.stop()
		t.Fatalf("serve listens on %s, want the saved %s", instance.url, address)
	}
	for _, host := range []string{"gitbox.test:" + port, "localhost:" + port, address, "[::1]:" + port} {
		if status := statusForHost(t, instance.url, host); status == http.StatusMisdirectedRequest {
			instance.stop()
			t.Fatalf("Host %s refused", host)
		}
	}
	if status := statusForHost(t, instance.url, "other.test:"+port); status != http.StatusMisdirectedRequest {
		instance.stop()
		t.Fatalf("unknown Host status=%d", status)
	}
	report := networkJSON(t, stateDir)
	if report.Server != "running" || report.Running == nil || report.RestartNeeded ||
		report.Running.Listen != address || report.Running.Address != address || report.Running.ListenSource != "saved" ||
		report.Running.BaseURL != "http://gitbox.test:"+port || report.Running.BaseURLSource != "saved" {
		instance.stop()
		t.Fatalf("running report=%+v running=%+v", report, report.Running)
	}
	// A saved name the server already accepts needs no restart; a new one,
	// or the removal of one loaded at start, does.
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--allowed-host", "gitbox.test")
	noErr(t, err)
	if report := networkJSON(t, stateDir); report.RestartNeeded {
		instance.stop()
		t.Fatal("saving a name the server already accepts asked for a restart")
	}
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--allowed-host", "later.test")
	noErr(t, err)
	if report := networkJSON(t, stateDir); !report.RestartNeeded {
		instance.stop()
		t.Fatal("a saved change while running did not ask for a restart")
	}
	text, err := runNetwork(t, "show", "--state-dir", stateDir)
	noErr(t, err)
	if !strings.Contains(text, "Running server") || !strings.Contains(text, "Restart OwnGit to apply") {
		instance.stop()
		t.Fatalf("show text: %q", text)
	}
	instance.stop()
	if report := networkJSON(t, stateDir); report.Server != "not_running" || report.Running != nil {
		t.Fatalf("after stop: %+v", report)
	}
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	_, found, err := store.RunningNetwork(context.Background())
	noErr(t, store.Close())
	if err != nil || found {
		t.Fatalf("running record left after stop: found=%v err=%v", found, err)
	}

	// A flag overrides the saved value for one run only.
	instance = startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--listen", "127.0.0.1:0"})
	if instance.url == "http://"+address {
		instance.stop()
		t.Fatal("the --listen flag did not override the saved address")
	}
	report = networkJSON(t, stateDir)
	if report.Running == nil || report.Running.ListenSource != "flag" || report.Saved.Listen != address || report.Running.BaseURLSource != "saved" || report.RestartNeeded {
		instance.stop()
		t.Fatalf("flag run report=%+v running=%+v", report, report.Running)
	}
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--remove-allowed-host", "later.test")
	noErr(t, err)
	report = networkJSON(t, stateDir)
	instance.stop()
	if !report.RestartNeeded {
		t.Fatal("removing a name the server loaded at start did not ask for a restart")
	}

	// A saved address that cannot be used names the recovery, which works.
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--listen", "192.0.2.1:"+strconv.Itoa(7729))
	noErr(t, err)
	err = serveWithContext(context.Background(), []string{"--state-dir", stateDir, "--no-open"}, func(string) error { return nil }, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "owngit network reset") {
		t.Fatalf("unusable saved address: err=%v", err)
	}
	_, err = runNetwork(t, "reset", "--state-dir", stateDir)
	noErr(t, err)
	if report := networkJSON(t, stateDir); report.NextStart.Listen != "127.0.0.1:7654" || report.Saved.BaseURL != "" {
		t.Fatalf("after reset: %+v", report)
	}
}

// A record is reported as running only while the serve process that
// published it is alive. A record left by a crash is ignored, also when
// another process such as an older OwnGit or an offline backup holds the
// state directory.
func TestNetworkShowTrustsTheRecordOnlyWhileItsServerLives(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	noErr(t, store.PublishRunningNetwork(context.Background(), state.RunningNetwork{PID: 1, Listen: "127.0.0.1:7752", Address: "127.0.0.1:7752", ListenSource: "flag"}))
	noErr(t, store.Close())

	if report := networkJSON(t, stateDir); report.Server != "not_running" || report.Running != nil || !report.StaleRecord {
		t.Fatalf("crash record without any holder: %+v", report)
	}
	release, err := state.AcquireOfflineLock(stateDir)
	noErr(t, err)
	report := networkJSON(t, stateDir)
	text, err := runNetwork(t, "show", "--state-dir", stateDir)
	release()
	noErr(t, err)
	if report.Server != "unknown" || report.Running != nil || !report.StaleRecord || strings.Contains(text, "Running server") || !strings.Contains(text, "was ignored") {
		t.Fatalf("crash record with a foreign lock holder: %+v\n%s", report, text)
	}

	// A live holder of the running-record lock vouches for its record.
	releaseRecord, err := state.AcquireExclusiveFileLock(filepath.Join(stateDir, state.RunningNetworkLockFile))
	noErr(t, err)
	if report := networkJSON(t, stateDir); report.Server != "running" || report.Running == nil || report.StaleRecord {
		releaseRecord()
		t.Fatalf("record of a live server: %+v", report)
	}
	store, err = state.Open(context.Background(), stateDir)
	noErr(t, err)
	noErr(t, store.ClearRunningNetwork(context.Background()))
	noErr(t, store.Close())
	report = networkJSON(t, stateDir)
	releaseRecord()
	if report.Server != "starting" || report.Running != nil {
		t.Fatalf("live server without a record yet: %+v", report)
	}
}

// A serve that cannot take the running-record lock publishes no record: a
// reader would attribute it to whoever holds the lock, and after this run it
// would only be reported as stale.
func TestServeWithoutTheRunningLockPublishesNoRecord(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	noErr(t, os.MkdirAll(stateDir, 0o700))
	releaseRecord, err := state.AcquireExclusiveFileLock(filepath.Join(stateDir, state.RunningNetworkLockFile))
	noErr(t, err)
	defer releaseRecord()
	instance := startServed(t, stateDir)
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	_, published, err := store.RunningNetwork(context.Background())
	noErr(t, store.Close())
	noErr(t, err)
	instance.stop()
	if published {
		t.Fatalf("a serve without the running-record lock published a record\n%s", instance.log())
	}
	if !strings.Contains(instance.log(), "could not take the running-settings lock") {
		t.Fatalf("the log does not say why nothing is reported:\n%s", instance.log())
	}
}

// A serve that starts while "network show" probes its locks waits for the
// probe instead of failing, and it removes a record left by a crash as soon
// as it holds the running-record lock.
func TestServeWaitsForAMomentaryLockProbeAndClearsAStaleRecord(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	noErr(t, store.PublishRunningNetwork(context.Background(), state.RunningNetwork{PID: 1, Listen: "192.0.2.1:1", ListenSource: "flag"}))
	noErr(t, store.Close())
	release, err := state.AcquireOfflineLock(stateDir)
	noErr(t, err)
	releaseRecord, err := state.AcquireExclusiveFileLock(filepath.Join(stateDir, state.RunningNetworkLockFile))
	noErr(t, err)
	go func() {
		time.Sleep(100 * time.Millisecond)
		release()
		releaseRecord()
	}()
	instance := startServed(t, stateDir)
	report := networkJSON(t, stateDir)
	instance.stop()
	if report.Server != "running" || report.Running == nil || report.Running.PID != os.Getpid() || report.Running.Listen != "127.0.0.1:0" {
		t.Fatalf("report=%+v running=%+v", report, report.Running)
	}
}
