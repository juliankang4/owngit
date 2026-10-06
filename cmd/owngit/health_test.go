package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/state"
)

func TestHealthCommandRejectsAnotherProgram(t *testing.T) {
	health := useFakeHealth(t)
	health.answering = true
	stateDir := filepath.Join(t.TempDir(), "state")
	address := "127.0.0.1:18963"
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", address)
	noErr(t, err)

	output, err := captureStdout(func() error { return run([]string{"health", "--state-dir", stateDir}) })
	if err == nil || !strings.Contains(err.Error(), "another program answers at http://"+address) || output != "" {
		t.Fatalf("health with another program: output %q, error %v", output, err)
	}
	if len(health.checked) != 1 || health.checked[0] != address {
		t.Fatalf("health checked %q, want only %s", health.checked, address)
	}

	health.answering = false
	output, err = captureStdout(func() error { return run([]string{"health", "--state-dir", stateDir}) })
	if err == nil || !strings.Contains(err.Error(), "OwnGit does not answer at http://"+address) || output != "" {
		t.Fatalf("health without a responder: output %q, error %v", output, err)
	}
}

func TestHealthCommandRefusesUnusableState(t *testing.T) {
	defaultDir := isolateDefaultState(t)
	health := useFakeHealth(t)
	health.answering = true
	regularFile := filepath.Join(t.TempDir(), "file")
	noErr(t, os.WriteFile(regularFile, []byte("not a state directory"), 0600))
	emptyDir := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	backslashes := filepath.Join(t.TempDir(), `state\copy\path`)
	for _, test := range []struct {
		name      string
		arguments []string
		stateDir  string
	}{
		{"regular file", []string{"--state-dir", regularFile}, regularFile},
		{"empty directory", []string{"--state-dir", emptyDir}, emptyDir},
		{"missing explicit state", []string{"--state-dir", missing}, missing},
		{"missing default state", nil, defaultDir},
		{"backslashes in missing path", []string{"--state-dir", backslashes}, backslashes},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := captureStdout(func() error { return run(append([]string{"health"}, test.arguments...)) })
			if err == nil || !strings.Contains(err.Error(), test.stateDir) || output != "" {
				t.Fatalf("unusable state: output %q, error %v", output, err)
			}
			if len(health.checked) != 0 {
				t.Fatalf("health fell back to checking %q", health.checked)
			}
		})
	}
	for _, dir := range []string{missing, defaultDir} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("health created %s: %v", dir, err)
		}
	}
}

func TestHealthCommandDoesNotMisidentifyAStartingServer(t *testing.T) {
	health := useFakeHealth(t)
	health.answering = true
	stateDir := filepath.Join(t.TempDir(), "state")
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "127.0.0.1:18964")
	noErr(t, err)
	store, err := openLiveState(context.Background(), stateDir)
	noErr(t, err)
	defer store.Close()
	release, err := store.ClaimRunningNetwork(context.Background())
	noErr(t, err)
	defer release()

	output, err := captureStdout(func() error { return run([]string{"health", "--state-dir", stateDir}) })
	if err == nil || !strings.Contains(err.Error(), "OwnGit is still starting") || !strings.Contains(err.Error(), "try again shortly") || strings.Contains(err.Error(), "another program") || output != "" {
		t.Fatalf("health while starting: output %q, error %v", output, err)
	}
}

func TestHealthCommandDoesNotTrustAnUnknownStateHolder(t *testing.T) {
	health := useFakeHealth(t)
	health.answering = true
	stateDir := filepath.Join(t.TempDir(), "state")
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "127.0.0.1:18965")
	noErr(t, err)
	release, err := state.AcquireOfflineLock(stateDir)
	noErr(t, err)
	defer release()

	output, err := captureStdout(func() error { return run([]string{"health", "--state-dir", stateDir}) })
	if err == nil || !strings.Contains(err.Error(), "cannot confirm") || strings.Contains(err.Error(), "another program answers") || output != "" {
		t.Fatalf("health with an unknown holder: output %q, error %v", output, err)
	}
}

func publishedHealthRun(t *testing.T, health *fakeHealth, record state.RunningNetwork) string {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "127.0.0.1:18966")
	noErr(t, err)
	store, err := openLiveState(context.Background(), stateDir)
	noErr(t, err)
	t.Cleanup(func() { noErr(t, store.Close()) })
	release, err := store.ClaimRunningNetwork(context.Background())
	noErr(t, err)
	t.Cleanup(release)
	noErr(t, store.PublishRunningNetwork(context.Background(), record))
	held, err := state.OpenStateDirectory(stateDir)
	noErr(t, err)
	defer held.Close()
	run, err := state.PublishHealthRun(held, record.Listen, record.Address)
	noErr(t, err)
	health.key = run.Key
	return stateDir
}

func runHealthCheck(t *testing.T, command, stateDir string) (string, error) {
	t.Helper()
	if command == "waitHealthy" {
		return waitHealthy(stateDir, 0)
	}
	return captureStdout(func() error { return run([]string{"health", "--state-dir", stateDir}) })
}

func TestHealthUsesBoundAddress(t *testing.T) {
	for _, command := range []string{"health", "waitHealthy"} {
		t.Run(command, func(t *testing.T) {
			for _, test := range []struct {
				name, listen, bound, target, host, legacy string
			}{
				{"hostname with IPv4", "elsewhere.invalid:0", "127.0.0.1:18966", "127.0.0.1:18966", "elsewhere.invalid:18966", "elsewhere.invalid:18966"},
				{"hostname with IPv6", "elsewhere.invalid:0", "[::1]:18966", "[::1]:18966", "elsewhere.invalid:18966", "elsewhere.invalid:18966"},
				{"numeric listen", "127.0.0.1:0", "127.0.0.1:18966", "127.0.0.1:18966", "127.0.0.1:18966", "127.0.0.1:18966"},
				{"IPv4 wildcard", "0.0.0.0:0", "0.0.0.0:18966", "127.0.0.1:18966", "127.0.0.1:18966", "127.0.0.1:18966"},
				{"IPv6 bound wildcard", "0.0.0.0:0", "[::]:18966", "[::1]:18966", "[::1]:18966", "127.0.0.1:18966"},
				{"hostname is not a bound IP", "127.0.0.1:0", "elsewhere.invalid:18966", "", "", "127.0.0.1:18966"},
			} {
				t.Run(test.name, func(t *testing.T) {
					health := useFakeHealth(t)
					health.answering = true
					stateDir := publishedHealthRun(t, health, state.RunningNetwork{
						PID: os.Getpid(), StartedAt: 100, Listen: test.listen, Address: test.bound,
					})
					output, err := runHealthCheck(t, command, stateDir)
					if test.target == "" {
						if err == nil || output != "" || len(health.checked) != 0 {
							t.Fatalf("invalid bound address: output %q, error %v, requests %q", output, err, health.checked)
						}
					} else {
						noErr(t, err)
						want := test.target
						if command == "health" {
							want = "OwnGit answers at http://" + test.target + "\n"
						}
						if output != want || len(health.checked) != 1 || health.checked[0] != test.target || health.hosts[0] != test.host {
							t.Fatalf("bound target: output %q, requests %q, Host headers %q, want %q with Host %q", output, health.checked, health.hosts, want, test.host)
						}
					}
					target, running, err := healthAddress(stateDir)
					noErr(t, err)
					if !running || target != test.legacy {
						t.Fatalf("legacy address selection changed: %q, running=%v, want %q", target, running, test.legacy)
					}
				})
			}
		})
	}
}

func TestHealthRejectsAnAnswerWithoutTheRunsProof(t *testing.T) {
	for _, command := range []string{"health", "waitHealthy"} {
		t.Run(command, func(t *testing.T) {
			for _, test := range []struct {
				name, key, want string
				withoutFile     bool
			}{
				{"no proof", "", "another program answers", false},
				{"another key", "another run's key", "another program answers", false},
				// A server that an older version started, still running.
				{"running record without a health file", "", "restart OwnGit", true},
			} {
				t.Run(test.name, func(t *testing.T) {
					health := useFakeHealth(t)
					health.answering = true
					stateDir := publishedHealthRun(t, health, state.RunningNetwork{Listen: "127.0.0.1:18966", Address: "127.0.0.1:18966"})
					health.key = test.key
					if test.withoutFile {
						noErr(t, os.Remove(filepath.Join(stateDir, state.HealthRunFile)))
					}
					output, err := runHealthCheck(t, command, stateDir)
					if err == nil || !strings.Contains(err.Error(), test.want) || output != "" {
						t.Fatalf("answer without the proof: output %q, error %v, want %q", output, err, test.want)
					}
				})
			}
		})
	}
}

func TestHealthCommandWithAStoppedHostname(t *testing.T) {
	health := useFakeHealth(t)
	health.answering = true
	stateDir := filepath.Join(t.TempDir(), "state")
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "localhost:18968")
	noErr(t, err)
	output, err := captureStdout(func() error { return run([]string{"health", "--state-dir", stateDir}) })
	if err == nil || !strings.Contains(err.Error(), "OwnGit is not running for state directory") || !strings.Contains(err.Error(), stateDir) || output != "" || len(health.checked) != 0 {
		t.Fatalf("stopped hostname: output %q, error %v, requests %q", output, err, health.checked)
	}
	target, running, err := healthAddress(stateDir)
	noErr(t, err)
	if running || target != "localhost:18968" {
		t.Fatalf("saved-address selection changed: %q, running=%v", target, running)
	}
}

type recordingHealthTransport struct {
	next    http.RoundTripper
	checked []string
	hosts   []string
}

func (transport *recordingHealthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.checked = append(transport.checked, request.URL.Host)
	transport.hosts = append(transport.hosts, request.Host)
	return transport.next.RoundTrip(request)
}

func TestHealthWithAHostnameAndBaseURL(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	_, port, err := net.SplitHostPort(freeLoopbackAddress(t))
	noErr(t, err)
	listen := net.JoinHostPort("localhost", port)
	tailscalePath := filepath.Join(t.TempDir(), "no-tailscale")
	instance := startServedWith(t, []string{"--state-dir", stateDir, "--listen", listen,
		"--base-url", "http://owner.example:" + port, "--tailscale", tailscalePath, "--no-open", "--no-update-check"})
	defer instance.stop()
	output, err := captureStdout(func() error {
		return run([]string{"tailscale", "status", "--state-dir", stateDir, "--tailscale", tailscalePath, "--json"})
	})
	noErr(t, err)
	if !strings.Contains(output, `"installed": false`) || !strings.Contains(output, `"on": false`) {
		t.Fatalf("Tailscale isolation: %s", output)
	}
	previous := healthClient
	transport := &recordingHealthTransport{next: previous.Transport}
	healthClient = &http.Client{Timeout: previous.Timeout, Transport: transport, CheckRedirect: previous.CheckRedirect}
	t.Cleanup(func() { healthClient = previous })
	_, _, observed, err := healthStatus(stateDir)
	noErr(t, err)
	target := observed.Record.Address
	// Health reads the server's own file and must not change the state
	// directory; a loosened mode that it repaired would show a write.
	if runtime.GOOS != "windows" {
		noErr(t, os.Chmod(stateDir, 0o750))
		defer func() {
			if info, err := os.Stat(stateDir); err != nil || info.Mode().Perm() != 0o750 {
				t.Errorf("health changed the state directory mode: %v, %v", info, err)
			}
		}()
	}
	for _, command := range []string{"health", "waitHealthy"} {
		output, err := runHealthCheck(t, command, stateDir)
		noErr(t, err)
		want := target
		if command == "health" {
			want = "OwnGit answers at http://" + target + "\n"
		}
		if output != want {
			t.Fatalf("%s output %q, want %q", command, output, want)
		}
	}
	if len(transport.checked) != 2 {
		t.Fatalf("health requests: %q", transport.checked)
	}
	for index, address := range transport.checked {
		host, _, err := net.SplitHostPort(address)
		noErr(t, err)
		if _, err := netip.ParseAddr(host); err != nil || address != target || transport.hosts[index] != listen {
			t.Fatalf("health connected to %s with Host %q, want numeric %s with Host %q", address, transport.hosts[index], target, listen)
		}
	}
}
