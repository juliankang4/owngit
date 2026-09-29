//go:build linux

package tray

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
if [ "$FAKE_MODE" = open ]; then
	echo '{"type":"open"}'
	while read -r line; do printf '%s\n' "$line" >> "$FAKE_DIR/after"; done
	exit 0
fi
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
	// While hidden, the next readings start no panel program again.
	os.Remove(filepath.Join(fakeDir, "init"))
	select {
	case err := <-result:
		t.Fatalf("the hidden icon ended: %v", err)
	case <-time.After(pollHidden + time.Second):
	}
	if _, err := os.Stat(filepath.Join(fakeDir, "init")); !os.IsNotExist(err) {
		t.Fatal("the hidden icon started the panel program again")
	}
	close(stop)
	if err := <-result; err != nil {
		t.Fatalf("the icon ended with %v", err)
	}
}

// An icon that starts hidden starts no panel program until it is shown.
func TestLinuxIconStartsHidden(t *testing.T) {
	fakeDir := useFakeGJS(t, "hide")
	stateDir := newStateDir(t)
	held, err := state.OpenStateDirectory(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	err = state.SetTrayHidden(held, true)
	held.Close()
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- Run(Options{StateDir: stateDir, Stop: stop}) }()
	select {
	case err := <-result:
		t.Fatalf("the hidden icon ended: %v", err)
	case <-time.After(pollHidden + time.Second):
	}
	if _, err := os.Stat(filepath.Join(fakeDir, "init")); !os.IsNotExist(err) {
		t.Fatal("the hidden icon started the panel program")
	}
	close(stop)
	if err := <-result; err != nil {
		t.Fatalf("the icon ended with %v", err)
	}
}

// Open dashboard without a proven answer opens nothing and shows the
// panel with Status unavailable at once.
func TestLinuxIconShowsAnUnprovenOpen(t *testing.T) {
	fakeDir := useFakeGJS(t, "open")
	stop := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- Run(Options{StateDir: newStateDir(t), Stop: stop}) }()
	deadline := time.Now().Add(time.Minute)
	for {
		after, _ := os.ReadFile(filepath.Join(fakeDir, "after"))
		if strings.Contains(string(after), `"open":true`) {
			if !strings.Contains(string(after), `"condition":"unavailable"`) || strings.Contains(string(after), `"type":"opened"`) {
				t.Fatalf("after Open:\n%s", after)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no answer to Open:\n%s", after)
		}
		time.Sleep(50 * time.Millisecond)
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

// The icon runs only a gjs that no other account can replace: the file,
// every folder above it and every link on the way.
func TestLinuxIconRunsOnlyAProtectedGJS(t *testing.T) {
	root := t.TempDir()
	mark := filepath.Join(root, "ran")
	program := []byte("#!/bin/sh\necho ran > " + mark + "\nexit 0\n")
	folder := func(name string, mode os.FileMode) string {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	write := func(path string, mode os.FileMode) {
		if err := os.WriteFile(path, program, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	own := folder("own", 0o755)
	write(filepath.Join(own, "gjs"), 0o755)
	shared := folder("shared", 0o777)
	write(filepath.Join(shared, "gjs"), 0o755)
	writable := folder("writable", 0o755)
	write(filepath.Join(writable, "gjs"), 0o757)
	linked := folder("linked", 0o755)
	if err := os.Symlink(filepath.Join(shared, "gjs"), filepath.Join(linked, "gjs")); err != nil {
		t.Fatal(err)
	}
	system, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, path string
		safe       bool
	}{
		{"a system program", system, true},
		{"a program in a folder of this account", filepath.Join(own, "gjs"), true},
		{"a folder every account can write", filepath.Join(shared, "gjs"), false},
		{"a file every account can write", filepath.Join(writable, "gjs"), false},
		{"a link into a folder every account can write", filepath.Join(linked, "gjs"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := protectedProgram(test.path)
			if test.safe {
				if err != nil || !filepath.IsAbs(resolved) {
					t.Fatalf("refused: %q, %v", resolved, err)
				}
				return
			}
			if !errors.Is(err, ErrUnsafeToolkit) || !strings.Contains(err.Error(), test.path) {
				t.Fatalf("accepted or unclear: %q, %v", resolved, err)
			}
			t.Setenv("PATH", filepath.Dir(test.path))
			problem := IconProblem()
			runErr := Run(Options{StateDir: newStateDir(t), Stop: make(chan struct{})})
			if !strings.Contains(problem, test.path) || !errors.Is(runErr, ErrUnsafeToolkit) || strings.Contains(problem, "\n") {
				t.Fatalf("problem %q, run %v", problem, runErr)
			}
			if _, err := os.Stat(mark); !os.IsNotExist(err) {
				t.Fatal("the unsafe gjs ran")
			}
		})
	}
}
