package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	for _, test := range []struct {
		name      string
		arguments []string
		stateDir  string
	}{
		{"regular file", []string{"--state-dir", regularFile}, regularFile},
		{"empty directory", []string{"--state-dir", emptyDir}, emptyDir},
		{"missing explicit state", []string{"--state-dir", missing}, missing},
		{"missing default state", nil, defaultDir},
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

func publishedHealthRun(t *testing.T, record state.RunningNetwork) (string, *state.Store, func()) {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	_, err := runNetwork(t, "set", "--state-dir", stateDir, "--listen", "127.0.0.1:18966")
	noErr(t, err)
	store, err := openLiveState(context.Background(), stateDir)
	noErr(t, err)
	t.Cleanup(func() { noErr(t, store.Close()) })
	release, err := store.ClaimRunningNetwork(context.Background())
	noErr(t, err)
	var once sync.Once
	stop := func() { once.Do(release) }
	t.Cleanup(stop)
	noErr(t, store.PublishRunningNetwork(context.Background(), record))
	return stateDir, store, stop
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
				name, listen, bound, target string
			}{
				{"hostname with IPv4", "elsewhere.invalid:0", "127.0.0.1:18966", "127.0.0.1:18966"},
				{"hostname with IPv6", "elsewhere.invalid:0", "[::1]:18966", "[::1]:18966"},
				{"IPv4 wildcard", "0.0.0.0:0", "0.0.0.0:18966", "127.0.0.1:18966"},
				{"IPv6 bound wildcard", "0.0.0.0:0", "[::]:18966", "[::1]:18966"},
				{"hostname is not a bound IP", "127.0.0.1:0", "elsewhere.invalid:18966", ""},
			} {
				t.Run(test.name, func(t *testing.T) {
					health := useFakeHealth(t)
					health.answering = true
					stateDir, _, _ := publishedHealthRun(t, state.RunningNetwork{
						PID: os.Getpid(), StartedAt: 100, Listen: test.listen, Address: test.bound,
					})
					output, err := runHealthCheck(t, command, stateDir)
					if test.target == "" {
						if err == nil || output != "" || len(health.checked) != 0 {
							t.Fatalf("invalid bound address: output %q, error %v, requests %q", output, err, health.checked)
						}
						return
					}
					noErr(t, err)
					want := test.target
					if command == "health" {
						want = "OwnGit answers at http://" + test.target + "\n"
					}
					if output != want || len(health.checked) != 1 || health.checked[0] != test.target {
						t.Fatalf("bound target: output %q, requests %q, want %q", output, health.checked, want)
					}
				})
			}
		})
	}
}

func TestHealthRejectsAChangedRun(t *testing.T) {
	for _, command := range []string{"health", "waitHealthy"} {
		t.Run(command, func(t *testing.T) {
			for _, change := range []string{"PID", "start time", "bound address", "released lock", "cleared record"} {
				t.Run(change, func(t *testing.T) {
					health := useFakeHealth(t)
					health.answering = true
					record := state.RunningNetwork{PID: os.Getpid(), StartedAt: 100, Listen: "127.0.0.1:18966", Address: "127.0.0.1:18966"}
					stateDir, store, stop := publishedHealthRun(t, record)
					health.onRequest = func() {
						switch change {
						case "PID":
							record.PID++
						case "start time":
							record.StartedAt++
						case "bound address":
							record.Address = "127.0.0.1:18967"
						case "released lock":
							stop()
							return
						case "cleared record":
							noErr(t, store.ClearRunningNetwork(context.Background()))
							return
						}
						noErr(t, store.PublishRunningNetwork(context.Background(), record))
					}
					output, err := runHealthCheck(t, command, stateDir)
					if err == nil || !strings.Contains(err.Error(), "cannot confirm") || output != "" || len(health.checked) != 1 {
						t.Fatalf("changed run: output %q, error %v, requests %q", output, err, health.checked)
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
