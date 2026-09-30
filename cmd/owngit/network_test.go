package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/server"
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
		{"defaults", state.NetworkSettings{}, nil, serveNetwork{Listen: "127.0.0.1:7654", ListenSource: "default", BaseURL: "", BaseURLSource: "default"}},
		{"saved", saved, nil, serveNetwork{Listen: "0.0.0.0:7700", ListenSource: "saved", BaseURL: "http://gitbox.internal:7700", BaseURLSource: "saved"}},
		{"flags win", saved, []string{"--listen", "127.0.0.1:0", "--base-url", "http://other.internal:1"},
			serveNetwork{Listen: "127.0.0.1:0", ListenSource: "flag", BaseURL: "http://other.internal:1", BaseURLSource: "flag"}},
		{"flag equal to the default still wins", saved, []string{"--listen", "127.0.0.1:7654"},
			serveNetwork{Listen: "127.0.0.1:7654", ListenSource: "flag", BaseURL: "http://gitbox.internal:7700", BaseURLSource: "saved"}},
		{"empty base URL flag derives for this run", saved, []string{"--base-url", ""},
			serveNetwork{Listen: "0.0.0.0:7700", ListenSource: "saved", BaseURL: "", BaseURLSource: "flag"}},
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
	output, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "0.0.0.0:7720", "--accept-insecure-http")
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

	// An empty value removes one saved setting, written as the docs and
	// terminal setup print it, which every shell passes as one argument.
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--base-url=")
	noErr(t, err)
	if after := networkJSON(t, stateDir); after.Saved.BaseURL != "" || after.Saved.Listen != "0.0.0.0:7720" {
		t.Fatalf("after removing the base URL: %+v", after.Saved)
	}

	output, err = runNetwork(t, "reset", "--state-dir", stateDir)
	noErr(t, err)
	if !strings.Contains(output, "127.0.0.1:7654") || !strings.Contains(output, "Allowed Hosts kept: 100.64.0.7, gitbox.internal. localhost, 127.0.0.1 and ::1 are always accepted from this computer.") {
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
	// Before the first start, show reports the defaults, as text and as
	// JSON, and creates nothing.
	missing := filepath.Join(t.TempDir(), "missing")
	output, err = runNetwork(t, "show", "--state-dir", missing)
	if err != nil || !strings.Contains(output, "No OwnGit state exists in "+missing) || !strings.Contains(output, "not saved, default 127.0.0.1:7654") {
		t.Fatalf("show on a missing state: %q %v", output, err)
	}
	output, err = runNetwork(t, "show", "--state-dir", missing, "--json")
	noErr(t, err)
	var defaults struct {
		networkReport
		StateMissing bool `json:"state_missing"`
	}
	if err := json.Unmarshal([]byte(output), &defaults); err != nil || !defaults.StateMissing || defaults.NextStart.Listen != "127.0.0.1:7654" ||
		defaults.Saved.AllowedHosts == nil || defaults.Server != server.NetworkNotRunning {
		t.Fatalf("show --json on a missing state: %q %v", output, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("show created the missing state directory")
	}
	// Under --json a failure is a JSON error object.
	_, err = runNetwork(t, "show", "--state-dir", stateDir, "--json", "extra")
	var printed strings.Builder
	if err == nil || !writeStructuredCommandError(&printed, err) || !strings.Contains(printed.String(), `"code":"invalid_arguments"`) {
		t.Fatalf("show --json with an extra argument: %v %q", err, printed.String())
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
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--listen", "192.0.2.1:"+strconv.Itoa(7729), "--accept-insecure-http")
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

// A listen address beyond this computer is saved only after the owner
// accepted plain HTTP, once, as in Settings; nothing is saved before that.
func TestNetworkSetAsksOnceToAcceptPlainHTTP(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	saved := func() (state.NetworkSettings, bool) {
		t.Helper()
		store, err := state.Open(context.Background(), stateDir)
		noErr(t, err)
		defer store.Close()
		network, err := store.NetworkSettings(context.Background())
		noErr(t, err)
		settings, err := store.Settings(context.Background())
		noErr(t, err)
		return network, settings.InsecureHTTPAccepted
	}
	if _, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "127.0.0.1:7721"); err != nil {
		t.Fatalf("a loopback address: %v", err)
	}
	if network, accepted := saved(); network.Listen != "127.0.0.1:7721" || accepted {
		t.Fatalf("loopback saved %+v accepted=%v", network, accepted)
	}
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "0.0.0.0:7720", "--base-url", "http://gitbox.internal:7720")
	if err == nil || !strings.Contains(err.Error(), "--accept-insecure-http") {
		t.Fatalf("an address beyond this computer without the acknowledgement: %v", err)
	}
	if network, accepted := saved(); network.Listen != "127.0.0.1:7721" || network.BaseURL != "" || accepted {
		t.Fatalf("a refused change saved %+v accepted=%v", network, accepted)
	}
	if _, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "0.0.0.0:7720", "--accept-insecure-http"); err != nil {
		t.Fatalf("with the acknowledgement: %v", err)
	}
	if network, accepted := saved(); network.Listen != "0.0.0.0:7720" || !accepted {
		t.Fatalf("saved %+v accepted=%v", network, accepted)
	}
	// Once accepted, it is not asked again.
	if _, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "192.0.2.1:7722"); err != nil {
		t.Fatalf("after the acknowledgement: %v", err)
	}
}

// The command first-run setup prints for Tailscale devices works as printed
// after a setup that listened only on this computer, and records the plain
// HTTP acknowledgement.
func TestTheTailscaleCommandSetupPrintsIsAccepted(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(context.Background(), stateDir)
	noErr(t, err)
	noErr(t, store.CompleteSetup(context.Background(), filepath.Join(t.TempDir(), "repositories"), "open", "", "synthetic-admin-hash", false))
	noErr(t, store.Close())
	// The words internal/firstrun prints (see its flow test), without the
	// program name.
	printed := strings.Fields("network set --listen 100.64.0.7:7654 --base-url http://my-mac.tail0000.ts.net:7654 --accept-insecure-http")
	if _, err := captureStdout(func() error { return run(append(printed, "--state-dir", stateDir)) }); err != nil {
		t.Fatalf("the printed command was refused: %v", err)
	}
	store, err = state.Open(context.Background(), stateDir)
	noErr(t, err)
	defer store.Close()
	network, err := store.NetworkSettings(context.Background())
	noErr(t, err)
	settings, err := store.Settings(context.Background())
	noErr(t, err)
	if network.Listen != "100.64.0.7:7654" || !settings.InsecureHTTPAccepted {
		t.Fatalf("saved %+v accepted=%v", network, settings.InsecureHTTPAccepted)
	}
}

// network set --json and network reset --json print one JSON result: the
// settings as saved, which apply at the next start, the plain HTTP
// acknowledgement and warnings. A refusal is a JSON error and saves nothing.
func TestNetworkSetAndResetPrintJSON(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	type change struct {
		OK    bool `json:"ok"`
		Saved struct {
			Listen         string   `json:"listen"`
			BaseURL        string   `json:"base_url"`
			AllowedHosts   []string `json:"allowed_hosts"`
			TrustedProxies []string `json:"trusted_proxies"`
		} `json:"saved"`
		AppliesAtNextStart bool     `json:"applies_at_next_start"`
		PlainHTTPAccepted  bool     `json:"plain_http_accepted"`
		Warnings           []string `json:"warnings"`
	}
	decode := func(output string) change {
		t.Helper()
		var result change
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("output %q: %v", output, err)
		}
		return result
	}
	jsonError := func(err error) string {
		t.Helper()
		var output bytes.Buffer
		if reportError(&output, err) != 1 {
			t.Fatalf("exit status for %v", err)
		}
		var envelope struct {
			OK    bool `json:"ok"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(output.Bytes(), &envelope) != nil || envelope.OK {
			t.Fatalf("not a JSON error: %q", output.String())
		}
		return envelope.Error.Code
	}

	if _, err := runNetwork(t, "reset", "--state-dir", stateDir, "--json"); jsonError(err) != "state_unavailable" {
		t.Fatalf("reset before any state: %v", err)
	}
	if _, err := runNetwork(t, "set", "--state-dir", stateDir, "--json"); jsonError(err) != "invalid_arguments" {
		t.Fatalf("set without a setting: %v", err)
	}
	result := decode(noErrOutput(t)(runNetwork(t, "set", "--state-dir", stateDir, "--listen", "127.0.0.1:7730", "--allowed-host", "gitbox.internal", "--remove-trusted-proxy", "10.0.0.1", "--json")))
	if !result.OK || result.Saved.Listen != "127.0.0.1:7730" || !slices.Contains(result.Saved.AllowedHosts, "gitbox.internal") ||
		!result.AppliesAtNextStart || result.PlainHTTPAccepted || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "10.0.0.1 was not a trusted proxy") {
		t.Fatalf("set printed %+v", result)
	}
	if _, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "0.0.0.0:7730", "--json"); jsonError(err) != "acknowledgement_required" {
		t.Fatalf("an address beyond this computer without the acknowledgement: %v", err)
	}
	result = decode(noErrOutput(t)(runNetwork(t, "set", "--state-dir", stateDir, "--trusted-proxy", "10.0.0.2", "--json")))
	if result.Saved.Listen != "127.0.0.1:7730" || result.PlainHTTPAccepted {
		t.Fatalf("the refused change was saved: %+v", result)
	}
	result = decode(noErrOutput(t)(runNetwork(t, "set", "--state-dir", stateDir, "--listen", "0.0.0.0:7730", "--accept-insecure-http", "--json")))
	if result.Saved.Listen != "0.0.0.0:7730" || !result.PlainHTTPAccepted || len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "plain HTTP") {
		t.Fatalf("set with the acknowledgement printed %+v", result)
	}

	result = decode(noErrOutput(t)(runNetwork(t, "reset", "--state-dir", stateDir, "--json")))
	if result.Saved.Listen != "" || len(result.Saved.AllowedHosts) == 0 || len(result.Saved.TrustedProxies) != 1 || !result.PlainHTTPAccepted {
		t.Fatalf("reset printed %+v", result)
	}
	result = decode(noErrOutput(t)(runNetwork(t, "reset", "--state-dir", stateDir, "--clear-allowed-hosts", "--clear-trusted-proxies", "--json")))
	if len(result.Saved.AllowedHosts) != 0 || len(result.Saved.TrustedProxies) != 0 {
		t.Fatalf("reset with clears printed %+v", result)
	}
}

// noErrOutput fails the test on an error and returns the output.
func noErrOutput(t *testing.T) func(string, error) string {
	return func(output string, err error) string {
		t.Helper()
		noErr(t, err)
		return output
	}
}

// "network set" saves the public share address with its warnings and
// refuses one that is half set or shares OwnGit's own port. At the next
// start OwnGit answers share links only there, and when that address
// cannot listen, OwnGit still serves its own address and says why.
func TestServeOpensThePublicShareAddress(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	for name, arguments := range map[string][]string{
		"without a URL":             {"--public-share-listen", "127.0.0.1:7655"},
		"without an address":        {"--public-share-url", "https://share.example.test"},
		"on OwnGit's own port":      {"--public-share-listen", "127.0.0.1:7654", "--public-share-url", "https://share.example.test"},
		"with a path":               {"--public-share-listen", "127.0.0.1:7655", "--public-share-url", "https://share.example.test/x"},
		"turned on and off at once": {"--public-share-listen", "127.0.0.1:7655", "--public-share-url", "https://share.example.test", "--public-share-off"},
	} {
		if _, err := runNetwork(t, append([]string{"set", "--state-dir", stateDir}, arguments...)...); err == nil {
			t.Errorf("%s was saved", name)
		}
	}
	public := freeLoopbackAddress(t)
	output, err := runNetwork(t, "set", "--state-dir", stateDir, "--public-share-listen", public, "--public-share-url", "https://share.example.test")
	noErr(t, err)
	if !strings.Contains(output, "Anyone on the Internet can reach the public address") || !strings.Contains(output, "No reverse proxy is trusted") {
		t.Fatalf("set output: %q", output)
	}
	if report := networkJSON(t, stateDir); report.Saved.PublicShareListen != public || report.Saved.PublicShareURL != "https://share.example.test" {
		t.Fatalf("saved report=%+v", report.Saved)
	}

	instance := startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--listen", "127.0.0.1:0"})
	report := networkJSON(t, stateDir)
	if report.Running == nil || report.Running.PublicShareAddress != public || report.Running.PublicShareURL != "https://share.example.test" || report.RestartNeeded {
		instance.stop()
		t.Fatalf("running report=%+v", report.Running)
	}
	for path, want := range map[string]int{"/": http.StatusNotFound, "/setup": http.StatusNotFound, "/api/v1/repositories": http.StatusNotFound, "/assets/owngit.css": http.StatusOK} {
		response, err := http.Get("http://" + public + path)
		noErr(t, err)
		response.Body.Close()
		if response.StatusCode != want {
			instance.stop()
			t.Fatalf("%s status=%d, want %d", path, response.StatusCode, want)
		}
	}
	text, err := runNetwork(t, "show", "--state-dir", stateDir)
	noErr(t, err)
	// Turning it off while OwnGit runs waits for the next start, and the
	// answer says so.
	changed, err := runNetwork(t, "set", "--state-dir", stateDir, "--public-share-off", "--json")
	noErr(t, err)
	instance.stop()
	if !strings.Contains(text, "Public share address: listening on "+public) {
		t.Fatalf("show text: %q", text)
	}
	if !strings.Contains(changed, `"applies_at_next_start": true`) || !strings.Contains(changed, `"restart_needed": true`) {
		t.Fatalf("set --json while running: %s", changed)
	}
	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--public-share-listen", public, "--public-share-url", "https://share.example.test")
	noErr(t, err)

	// A public address that cannot listen leaves OwnGit's own address.
	taken, err := net.Listen("tcp", public)
	noErr(t, err)
	defer taken.Close()
	instance = startServedWith(t, []string{"--state-dir", stateDir, "--no-open", "--listen", "127.0.0.1:0"})
	report = networkJSON(t, stateDir)
	instance.stop()
	if report.Running == nil || report.Running.PublicShareAddress != "" || report.Running.PublicShareError == "" {
		t.Fatalf("running report with the port taken=%+v", report.Running)
	}

	_, err = runNetwork(t, "set", "--state-dir", stateDir, "--public-share-off")
	noErr(t, err)
	if report := networkJSON(t, stateDir); report.Saved.PublicShareListen != "" || report.Saved.PublicShareURL != "" {
		t.Fatalf("after turning off=%+v", report.Saved)
	}
}
