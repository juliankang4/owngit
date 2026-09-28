// Package tailscaletest provides a fake tailscale command for tests. No test
// runs the real tailscale or changes this computer's Tailscale settings.
//
// The fake is the test binary itself, linked under a private path for each
// fake: a package's TestMain calls RunIfFake, which acts as tailscale when
// the process runs from such a path and the arguments are a tailscale
// command. The state file beside that path says what the fake reports and
// records every call, so a test can check exactly which commands OwnGit ran.
// A command therefore reaches the state of the fake it was started from,
// whatever another test set up meanwhile.
//
// The test and every command it causes, each in its own process, read and
// change the state file only while holding the lock file next to it. On
// Windows a file that one process has open cannot be replaced by another,
// so a read beside a command's save would fail with a sharing violation.
package tailscaletest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"owngit/internal/state"
	"owngit/internal/tailscale"
)

// State is what the fake reports and what it was asked.
type State struct {
	// Status is printed for "status --json", unless StatusError is set.
	Status Status `json:"status"`
	// StatusError is printed to standard error for "status --json", which
	// then fails, like a client whose daemon does not run.
	StatusError string `json:"status_error,omitempty"`
	// Serve is the Serve configuration.
	Serve tailscale.ServeConfig `json:"serve"`
	// WriteError is printed to standard error for every Serve change, which
	// then fails, like an operator permission error.
	WriteError string `json:"write_error,omitempty"`
	// IgnoreWrites makes Serve changes succeed without being kept, as
	// Tailscale does when it cannot save its state.
	IgnoreWrites bool `json:"ignore_writes,omitempty"`
	// WriteDelay makes every Serve change wait this many milliseconds
	// before it takes effect, as Tailscale does while it fetches a
	// certificate. Other calls are answered meanwhile.
	WriteDelay int `json:"write_delay,omitempty"`
	// ReadDelay does the same for "status --json" and "serve status
	// --json", as a slow tailscaled does.
	ReadDelay int `json:"read_delay,omitempty"`
	// ServeReadErrorAfterWrite is printed to standard error for "serve
	// status --json" once a Serve change succeeded (Wrote), which then
	// fails, like a tailscaled that stops answering after a change.
	ServeReadErrorAfterWrite string `json:"serve_read_error_after_write,omitempty"`
	// Wrote is set when a Serve change succeeds.
	Wrote bool `json:"wrote,omitempty"`
	// WriteErrorAfterChange is printed to standard error after a Serve
	// change took effect, which then fails, like a command interrupted
	// after Tailscale kept the change.
	WriteErrorAfterChange string `json:"write_error_after_change,omitempty"`

	// Calls are the argument lists the fake was run with, in order.
	Calls [][]string `json:"calls,omitempty"`
	// Running counts the commands in progress, delays included, and
	// MaxRunning the most that were in progress at once, so a test can tell
	// whether commands overlapped. A command killed during its delay stays
	// counted.
	Running    int `json:"running,omitempty"`
	MaxRunning int `json:"max_running,omitempty"`

	// HoldReads makes "status --json" and "serve status --json" wait,
	// after any ReadDelay, until the test clears it, so a test can change
	// the state at a known point: a waiting read answers from the state as
	// it is when released. PassReads lets that many more reads answer
	// meanwhile, and HeldReads counts the reads waiting now.
	HoldReads bool `json:"hold_reads,omitempty"`
	PassReads int  `json:"pass_reads,omitempty"`
	HeldReads int  `json:"held_reads,omitempty"`
}

// Status is the subset of "tailscale status --json" the fake prints.
type Status struct {
	BackendState   string          `json:"BackendState"`
	Version        string          `json:"Version,omitempty"`
	Self           *Self           `json:"Self,omitempty"`
	CertDomains    []string        `json:"CertDomains,omitempty"`
	CurrentTailnet *CurrentTailnet `json:"CurrentTailnet,omitempty"`
}

type Self struct {
	DNSName      string   `json:"DNSName"`
	TailscaleIPs []string `json:"TailscaleIPs,omitempty"`
}

type CurrentTailnet struct {
	MagicDNSEnabled bool `json:"MagicDNSEnabled"`
}

// Name is the synthetic MagicDNS name of the fake computer, and IPv4 and
// IPv6 its Tailscale addresses.
const (
	Name = "gitbox.tail0000.ts.net"
	IPv4 = "100.64.0.7"
	IPv6 = "fd7a:115c:a1e0::7"
)

// Running is the status of a signed-in computer with MagicDNS and HTTPS
// certificates.
func Running() Status {
	return Status{
		BackendState: "Running", Version: "1.102.5-test",
		Self: &Self{DNSName: Name + ".", TailscaleIPs: []string{IPv4, IPv6}}, CertDomains: []string{Name},
		CurrentTailnet: &CurrentTailnet{MagicDNSEnabled: true},
	}
}

// Fake is one fake tailscale command and its state file.
type Fake struct {
	t *testing.T
	// Path runs the fake.
	Path string
	file string
}

// New creates a fake that reports state. Its executable and state file
// belong to this test.
func New(t *testing.T, state State) *Fake {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tailscale"+filepath.Ext(executable))
	if err := os.Link(executable, path); err != nil {
		// The build cache and the temporary folder can be on different
		// file systems.
		if err := copyExecutable(executable, path); err != nil {
			t.Fatal(err)
		}
	}
	fake := &Fake{t: t, Path: path, file: stateFile(path)}
	// No command can run before Path is returned, so this needs no lock.
	if err := store(fake.file, state); err != nil {
		t.Fatal(err)
	}
	return fake
}

func stateFile(executable string) string {
	return executable + ".json"
}

func copyExecutable(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}

// Update changes the fake's state.
func (fake *Fake) Update(change func(*State)) {
	fake.t.Helper()
	if err := update(fake.file, func(state *State) bool { change(state); return true }); err != nil {
		fake.t.Fatal(err)
	}
}

// State returns the fake's current state.
func (fake *Fake) State() State {
	fake.t.Helper()
	var current State
	if err := update(fake.file, func(state *State) bool { current = *state; return false }); err != nil {
		fake.t.Fatal(err)
	}
	return current
}

// AwaitHeldReads waits until n reads are held (HoldReads).
func (fake *Fake) AwaitHeldReads(n int) {
	fake.t.Helper()
	for deadline := time.Now().Add(time.Minute); fake.State().HeldReads < n; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			fake.t.Fatalf("fewer than %d Tailscale reads were held within a minute: %q", n, fake.Calls())
		}
	}
}

// Calls returns the commands run so far, each joined with spaces.
func (fake *Fake) Calls() []string {
	fake.t.Helper()
	var calls []string
	for _, call := range fake.State().Calls {
		calls = append(calls, strings.Join(call, " "))
	}
	return calls
}

// Writes returns the commands run so far that change Tailscale.
func (fake *Fake) Writes() []string {
	var writes []string
	for _, call := range fake.Calls() {
		if call != "version" && call != "status --json" && call != "serve status --json" {
			writes = append(writes, call)
		}
	}
	return writes
}

// update runs change on the state with the lock held, and saves the state
// when change returns true.
func update(file string, change func(*State) bool) error {
	unlock, err := lockFile(file + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	state, err := load(file)
	if err != nil {
		return err
	}
	if !change(&state) {
		return nil
	}
	return store(file, state)
}

func load(file string) (State, error) {
	var state State
	content, err := os.ReadFile(file)
	if err != nil {
		return state, err
	}
	return state, json.Unmarshal(content, &state)
}

// store writes the state in place. Every reader holds the lock, so none
// sees a half-written file. Replacing the file by a rename instead fails
// on Windows while any other handle is open on it, even one that only
// reads its attributes, such as another fake command checking that the
// file exists or a virus scanner looking at the last write.
func store(file string, state State) error {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, content, 0o600)
}

// RunIfFake acts as tailscale and exits when this process was started as
// a fake. Call it first in TestMain. A fake answers every invocation, so an
// unsupported command fails rather than running the tests.
func RunIfFake() {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	file := stateFile(executable)
	if _, err := os.Stat(file); err != nil {
		return
	}
	os.Exit(run(file, os.Args[1:]))
}

// run handles one fake command. It returns the command's exit code, or 3
// when the state cannot be read or saved.
func run(file string, arguments []string) int {
	write := len(arguments) > 1 && arguments[0] == "serve" && arguments[1] != "status"
	read := slices.Contains([]string{"status --json", "serve status --json"}, strings.Join(arguments, " "))
	var started State
	err := update(file, func(state *State) bool {
		state.Running++
		state.MaxRunning = max(state.MaxRunning, state.Running)
		started = *state
		return true
	})
	switch {
	case err != nil:
	case write && started.WriteDelay > 0:
		time.Sleep(time.Duration(started.WriteDelay) * time.Millisecond)
	case read && started.ReadDelay > 0:
		time.Sleep(time.Duration(started.ReadDelay) * time.Millisecond)
	}
	if err == nil && read {
		err = awaitRelease(file)
	}
	code := 0
	if err == nil {
		err = update(file, func(state *State) bool {
			state.Running--
			state.Calls = append(state.Calls, arguments)
			code = handle(state, arguments)
			return true
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	return code
}

// awaitRelease returns once this read may answer: nothing holds reads, or a
// pass is left (HoldReads).
func awaitRelease(file string) error {
	held := false
	for {
		released := false
		err := update(file, func(state *State) bool {
			if state.HoldReads && state.PassReads == 0 {
				if held {
					return false
				}
				held = true
				state.HeldReads++
				return true
			}
			released = true
			if state.HoldReads {
				state.PassReads--
			}
			if held {
				state.HeldReads--
			}
			return state.HoldReads || held
		})
		if err != nil || released {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func handle(state *State, arguments []string) int {
	command := strings.Join(arguments, " ")
	switch {
	case command == "version":
		fmt.Println("1.102.5-test")
		return 0
	case command == "status --json":
		if state.StatusError != "" {
			fmt.Fprintln(os.Stderr, state.StatusError)
			return 1
		}
		content, _ := json.MarshalIndent(state.Status, "", "  ")
		fmt.Println(string(content))
		return 0
	case command == "serve status --json":
		if state.Wrote && state.ServeReadErrorAfterWrite != "" {
			fmt.Fprintln(os.Stderr, state.ServeReadErrorAfterWrite)
			return 1
		}
		content, _ := json.MarshalIndent(state.Serve, "", "  ")
		fmt.Println(string(content))
		return 0
	}
	if len(arguments) == 4 && arguments[0] == "serve" && arguments[1] == "--bg" && strings.HasPrefix(arguments[2], "--https=") {
		if failed := state.writeRefused(); failed {
			return 1
		}
		state.addHandler(strings.TrimPrefix(arguments[2], "--https="), arguments[3])
		return state.changed()
	}
	if len(arguments) == 4 && arguments[0] == "serve" && strings.HasPrefix(arguments[1], "--https=") && arguments[2] == "--set-path=/" && arguments[3] == "off" {
		if failed := state.writeRefused(); failed {
			return 1
		}
		if !state.removeHandler(strings.TrimPrefix(arguments[1], "--https=")) {
			fmt.Fprintln(os.Stderr, "error: failed to remove web serve: handler does not exist")
			return 1
		}
		return state.changed()
	}
	fmt.Fprintf(os.Stderr, "fake tailscale: unsupported command %q\n", command)
	return 2
}

// changed finishes a Serve change that took effect.
func (state *State) changed() int {
	state.Wrote = true
	if state.WriteErrorAfterChange != "" {
		fmt.Fprintln(os.Stderr, state.WriteErrorAfterChange)
		return 1
	}
	return 0
}

func (state *State) writeRefused() bool {
	if state.WriteError != "" {
		fmt.Fprintln(os.Stderr, state.WriteError)
		return true
	}
	return false
}

func (state *State) name() string {
	if state.Status.Self == nil {
		return ""
	}
	return strings.TrimSuffix(state.Status.Self.DNSName, ".")
}

// addHandler does what "tailscale serve --bg --https=PORT TARGET" does.
func (state *State) addHandler(port, target string) {
	if state.IgnoreWrites {
		return
	}
	config := &state.Serve
	if config.TCP == nil {
		config.TCP = map[string]tailscale.TCPHandler{}
	}
	if config.Web == nil {
		config.Web = map[string]tailscale.WebServer{}
	}
	config.TCP[port] = tailscale.TCPHandler{HTTPS: true}
	key := net.JoinHostPort(state.name(), port)
	server := config.Web[key]
	if server.Handlers == nil {
		server.Handlers = map[string]tailscale.Handler{}
	}
	server.Handlers["/"] = tailscale.Handler{Proxy: target}
	config.Web[key] = server
	delete(config.AllowFunnel, key)
}

// removeHandler does what "tailscale serve --https=PORT --set-path=/ off"
// does.
func (state *State) removeHandler(port string) bool {
	key := net.JoinHostPort(state.name(), port)
	server, ok := state.Serve.Web[key]
	if !ok {
		return false
	}
	if _, ok := server.Handlers["/"]; !ok {
		return false
	}
	if state.IgnoreWrites {
		return true
	}
	delete(server.Handlers, "/")
	if len(server.Handlers) == 0 {
		delete(state.Serve.Web, key)
		delete(state.Serve.TCP, port)
		delete(state.Serve.AllowFunnel, key)
	} else {
		state.Serve.Web[key] = server
	}
	return true
}

// lockFile holds the operating system's lock on the file at path, waiting
// up to five seconds for another fake call to release it. The file is kept
// between calls: on Windows a name that was just deleted cannot be created
// again while any handle to it is still open, so a lock taken by creating
// and deleting a file failed now and then with "Access is denied".
func lockFile(path string) (func(), error) {
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(25 * time.Millisecond) {
		release, err := state.AcquireExclusiveFileLock(path)
		switch {
		case err == nil:
			return release, nil
		case !errors.Is(err, state.ErrInstanceRunning):
			return nil, fmt.Errorf("fake tailscale: lock %s: %w", path, err)
		case time.Now().After(deadline):
			return nil, fmt.Errorf("fake tailscale: lock %s is still held after five seconds", path)
		}
	}
}
