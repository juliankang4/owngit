//go:build linux

package tray

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
)

// fakeGJS stands in for the desktop's gjs: it answers the toolkit check,
// then speaks the panel program's side of the messages as FAKE_MODE says,
// and records what it was sent in FAKE_DIR.
const fakeGJS = `#!/bin/sh
[ "$1" = "-c" ] || exit 2
case "$2" in
"` + toolkitCheck + `") [ "$FAKE_MODE" = notoolkit ] && exit 1; exit 0 ;;
esac
read -r init
printf '%s\n' "$init" > "$FAKE_DIR/init"
if [ "$FAKE_MODE" = already ]; then
	echo '{"type":"error","code":"already_running","message":"the OwnGit icon already runs"}'
	exit 0
fi
echo '{"type":"ready"}'
read -r reading
printf '%s\n' "$reading" > "$FAKE_DIR/state"
echo '{"type":"hide"}'
while read -r _; do :; done
echo ended > "$FAKE_DIR/ended"
`

func useFakeGJS(t *testing.T, mode string) (fakeDir string) {
	t.Helper()
	fakeDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeDir, "gjs"), []byte(fakeGJS), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir)
	t.Setenv("FAKE_DIR", fakeDir)
	t.Setenv("FAKE_MODE", mode)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return fakeDir
}

func newStateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	store, err := state.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	return dir
}

// Hide in the panel is kept by owngit, not by the panel program: owngit
// writes the owner's choice, ends the panel program, and keeps running
// without an icon until it is stopped.
func TestLinuxIconKeepsTheHideChoice(t *testing.T) {
	fakeDir := useFakeGJS(t, "hide")
	stateDir := newStateDir(t)
	stop := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- Run(Options{StateDir: stateDir, Stop: stop}) }()

	deadline := time.Now().Add(time.Minute)
	for {
		if _, err := os.Stat(filepath.Join(fakeDir, "ended")); err == nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("the icon ended before it hid: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the panel program was not ended after Hide")
		}
		time.Sleep(50 * time.Millisecond)
	}
	held, err := state.OpenStateDirectory(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := state.TrayHidden(held)
	held.Close()
	if err != nil || !hidden {
		t.Fatalf("Hide was not kept: hidden=%v err=%v", hidden, err)
	}
	init, _ := os.ReadFile(filepath.Join(fakeDir, "init"))
	reading, _ := os.ReadFile(filepath.Join(fakeDir, "state"))
	if !strings.Contains(string(init), `"name":"app.owngit.Icon.S`) || !strings.Contains(string(reading), `"condition":"unavailable"`) ||
		!strings.Contains(string(reading), `"icon":"owngit-unavailable-symbolic"`) {
		t.Errorf("the panel program was sent\n%s%s", init, reading)
	}
	select {
	case err := <-result:
		t.Fatalf("the hidden icon ended: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(stop)
	if err := <-result; err != nil {
		t.Fatalf("the icon ended with %v", err)
	}
}

func TestLinuxIconRefusals(t *testing.T) {
	useFakeGJS(t, "already")
	if err := Run(Options{StateDir: newStateDir(t), Stop: make(chan struct{})}); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("a second icon: %v", err)
	}
	useFakeGJS(t, "notoolkit")
	if err := Run(Options{StateDir: newStateDir(t), Stop: make(chan struct{})}); !errors.Is(err, ErrNoToolkit) || IconProblem() == "" {
		t.Errorf("without GTK 4: %v, problem %q", err, IconProblem())
	}
	t.Setenv("PATH", t.TempDir())
	if err := Run(Options{StateDir: newStateDir(t), Stop: make(chan struct{})}); !errors.Is(err, ErrNoToolkit) {
		t.Errorf("without gjs: %v", err)
	}
}
