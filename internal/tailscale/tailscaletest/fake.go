// Package tailscaletest provides a fake tailscale command for tests. No test
// runs the real tailscale or changes this computer's Tailscale settings.
//
// The fake is the test binary itself: a package's TestMain calls RunIfFake,
// which acts as tailscale when the environment names a fake state file and
// the arguments are a tailscale command. The state file says what the fake
// reports and records every call, so a test can check exactly which
// commands OwnGit ran.
package tailscaletest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/tailscale"
)

// EnvState names the fake's state file. Set only by tests.
const EnvState = "OWNGIT_TEST_FAKE_TAILSCALE"

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
	// Calls are the argument lists the fake was run with, in order.
	Calls [][]string `json:"calls,omitempty"`
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
	DNSName string `json:"DNSName"`
}

type CurrentTailnet struct {
	MagicDNSEnabled bool `json:"MagicDNSEnabled"`
}

// Name is the synthetic MagicDNS name of the fake computer.
const Name = "gitbox.tail0000.ts.net"

// Running is the status of a signed-in computer with MagicDNS and HTTPS
// certificates.
func Running() Status {
	return Status{
		BackendState: "Running", Version: "1.102.5-test",
		Self: &Self{DNSName: Name + "."}, CertDomains: []string{Name},
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

// New creates a fake that reports state and sets EnvState for the test.
// Tests that use it cannot run in parallel.
func New(t *testing.T, state State) *Fake {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake := &Fake{t: t, Path: executable, file: filepath.Join(t.TempDir(), "fake-tailscale.json")}
	fake.save(state)
	t.Setenv(EnvState, fake.file)
	return fake
}

// Update changes the fake's state.
func (fake *Fake) Update(change func(*State)) {
	fake.t.Helper()
	state := fake.State()
	change(&state)
	fake.save(state)
}

// State returns the fake's current state.
func (fake *Fake) State() State {
	fake.t.Helper()
	state, err := load(fake.file)
	if err != nil {
		fake.t.Fatal(err)
	}
	return state
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

func (fake *Fake) save(state State) {
	fake.t.Helper()
	if err := store(fake.file, state); err != nil {
		fake.t.Fatal(err)
	}
}

var fileLock sync.Mutex

func load(file string) (State, error) {
	var state State
	content, err := os.ReadFile(file)
	if err != nil {
		return state, err
	}
	return state, json.Unmarshal(content, &state)
}

func store(file string, state State) error {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary := file + ".tmp"
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, file)
}

// RunIfFake acts as tailscale and exits when this process was started as
// the fake. Call it first in TestMain.
func RunIfFake() {
	file := os.Getenv(EnvState)
	if file == "" || len(os.Args) < 2 || !slices.Contains([]string{"version", "status", "serve", "funnel", "up", "down", "set", "cert"}, os.Args[1]) {
		return
	}
	os.Exit(run(file, os.Args[1:]))
}

// run handles one fake command. Calls from concurrent processes are
// serialized with a lock file next to the state.
func run(file string, arguments []string) int {
	if state, err := load(file); err == nil {
		write := len(arguments) > 1 && arguments[0] == "serve" && arguments[1] != "status"
		read := slices.Contains([]string{"status --json", "serve status --json"}, strings.Join(arguments, " "))
		switch {
		case write && state.WriteDelay > 0:
			time.Sleep(time.Duration(state.WriteDelay) * time.Millisecond)
		case read && state.ReadDelay > 0:
			time.Sleep(time.Duration(state.ReadDelay) * time.Millisecond)
		}
	}
	unlock, err := lockFile(file + ".lock")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	defer unlock()
	state, err := load(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	state.Calls = append(state.Calls, arguments)
	code := handle(&state, arguments)
	if err := store(file, state); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	return code
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
		content, _ := json.MarshalIndent(state.Serve, "", "  ")
		fmt.Println(string(content))
		return 0
	}
	if len(arguments) == 4 && arguments[0] == "serve" && arguments[1] == "--bg" && strings.HasPrefix(arguments[2], "--https=") {
		if failed := state.writeRefused(); failed {
			return 1
		}
		state.addHandler(strings.TrimPrefix(arguments[2], "--https="), arguments[3])
		return 0
	}
	if len(arguments) == 4 && arguments[0] == "serve" && strings.HasPrefix(arguments[1], "--https=") && arguments[2] == "--set-path=/" && arguments[3] == "off" {
		if failed := state.writeRefused(); failed {
			return 1
		}
		if !state.removeHandler(strings.TrimPrefix(arguments[1], "--https=")) {
			fmt.Fprintln(os.Stderr, "error: failed to remove web serve: handler does not exist")
			return 1
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "fake tailscale: unsupported command %q\n", command)
	return 2
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

// lockFile takes an exclusive lock by creating a file, waiting up to five
// seconds for another fake call to finish.
func lockFile(path string) (func(), error) {
	fileLock.Lock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			file.Close()
			return func() { _ = os.Remove(path); fileLock.Unlock() }, nil
		}
		if !errors.Is(err, os.ErrExist) || time.Now().After(deadline) {
			fileLock.Unlock()
			return nil, fmt.Errorf("fake tailscale: lock %s: %w", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
