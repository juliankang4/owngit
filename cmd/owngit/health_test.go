package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
