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

// After a Homebrew upgrade the state on disk has the old schema. Installing
// must hand off to brew without opening that state, since the new serving
// process backs it up and migrates it.
func TestHomebrewInstallDoesNotOpenAnExistingState(t *testing.T) {
	fixture := newInstallFixture(t, sshEnv, nil, false)
	config := t.TempDir()
	t.Setenv("HOME", config)
	t.Setenv("XDG_CONFIG_HOME", config)
	stateDir := defaultStateDir()
	noErr(t, os.MkdirAll(stateDir, 0o700))
	noErr(t, os.WriteFile(filepath.Join(stateDir, "owngit.sqlite"), []byte("old schema"), 0o600))
	var opened bool
	previous := openLiveStateAttempt
	t.Cleanup(func() { openLiveStateAttempt = previous })
	openLiveStateAttempt = func(context.Context, string) (*state.Store, error) {
		opened = true
		return nil, errors.New("state schema is older than this program")
	}
	fixture.host.homebrew = fakeBrewPrefix(t)

	_ = fixture.host.installHomebrew(stateDir, true)
	calls, _ := os.ReadFile(filepath.Join(fixture.host.homebrew, "bin", "brew.calls"))
	if opened || !strings.Contains(string(calls), "services restart owngit") {
		t.Fatalf("state opened=%v, brew calls %q, output:\n%s", opened, calls, fixture.out.String())
	}
}
