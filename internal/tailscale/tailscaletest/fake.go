// Package tailscaletest provides a fake Tailscale for tests: a fake
// LocalAPI and an empty file that stands for the tailscale command. No test
// reaches the real LocalAPI or changes this computer's Tailscale settings,
// and OwnGit never runs the command.
//
// The fake LocalAPI is a loopback HTTP server in the test process that
// answers Tailscale's status and the serve-config resource as Tailscale
// does, with a version (ETag) and a change applied only to the version it
// was made from (If-Match). Fake.Command returns a tailscale.Command that
// reaches it. The fake's state says what it reports and records every
// request, so a test can check exactly what OwnGit asked.
package tailscaletest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/tailscale"
)

// State is what the fake reports and what it was asked.
type State struct {
	// Status is the answer to a status read, unless StatusError is set.
	Status Status `json:"status"`
	// StatusError is the error the LocalAPI answers a status read with.
	StatusError string `json:"status_error,omitempty"`
	// NotRunning makes every connection to the LocalAPI fail, as when
	// tailscaled does not run.
	NotRunning bool `json:"not_running,omitempty"`
	// Serve is the Serve configuration, and ServeExtra its fields that
	// tailscale.ServeConfig does not name, such as Services, which the
	// LocalAPI keeps and reports as Tailscale keeps fields OwnGit does not
	// know.
	Serve      tailscale.ServeConfig      `json:"serve"`
	ServeExtra map[string]json.RawMessage `json:"serve_extra,omitempty"`
	// WriteDenied makes the LocalAPI refuse every Serve change as Tailscale
	// refuses a user who is not its operator.
	WriteDenied bool `json:"write_denied,omitempty"`
	// WriteError is the error the LocalAPI answers every Serve change with,
	// which then fails, like Tailscale when it is stopped.
	WriteError string `json:"write_error,omitempty"`
	// Unversioned makes the LocalAPI answer as Tailscale before 1.50 does:
	// a read of the Serve configuration has no version, and a change
	// applies whatever its If-Match header says.
	Unversioned bool `json:"unversioned,omitempty"`
	// ChangedBeforeWrite, when set, replaces the Serve configuration as the
	// next Serve change arrives, before Tailscale checks it, as another
	// program changing Serve at that moment; it is then cleared.
	ChangedBeforeWrite *tailscale.ServeConfig `json:"changed_before_write,omitempty"`
	// IgnoreWrites makes Serve changes succeed without being kept, as
	// Tailscale does when it cannot save its state.
	IgnoreWrites bool `json:"ignore_writes,omitempty"`
	// WriteDelay makes every Serve change wait this many milliseconds
	// before it takes effect, as Tailscale does while it fetches a
	// certificate. Other calls are answered meanwhile.
	WriteDelay int `json:"write_delay,omitempty"`
	// ReadDelay does the same for status reads and reads of the Serve
	// configuration, as a slow tailscaled does.
	ReadDelay int `json:"read_delay,omitempty"`
	// ServeReadErrorAfterWrite is the error the LocalAPI answers reads of
	// the Serve configuration with once a Serve change succeeded (Wrote),
	// like a tailscaled that stops answering after a change.
	ServeReadErrorAfterWrite string `json:"serve_read_error_after_write,omitempty"`
	// Wrote is set when a Serve change succeeds.
	Wrote bool `json:"wrote,omitempty"`
	// WriteErrorAfterChange is the error the LocalAPI answers a Serve change
	// with after the change took effect, like a connection interrupted after
	// Tailscale kept the change.
	WriteErrorAfterChange string `json:"write_error_after_change,omitempty"`

	// Calls are the LocalAPI requests (StatusRead, ServeRead, ServeWrite),
	// in order, and whatever a test adds.
	Calls [][]string `json:"calls,omitempty"`
	// Running counts the LocalAPI requests in progress, delays included, and
	// MaxRunning the most that were in progress at once, so a test can tell
	// whether calls overlapped. A request abandoned during its delay stays
	// counted until the delay ends.
	Running    int `json:"running,omitempty"`
	MaxRunning int `json:"max_running,omitempty"`

	// HoldReads makes status reads and reads of the Serve configuration
	// wait, after any ReadDelay, until the test clears it, so a test can
	// change the state at a known point: a waiting read answers from the
	// state as it is when released. PassReads lets that many more reads
	// answer meanwhile, and HeldReads counts the reads waiting now.
	HoldReads bool `json:"hold_reads,omitempty"`
	PassReads int  `json:"pass_reads,omitempty"`
	HeldReads int  `json:"held_reads,omitempty"`
}

// Status is the subset of Tailscale's status the fake answers.
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

// Calls recorded for LocalAPI requests.
const (
	StatusRead = "status GET"
	ServeRead  = "serve-config GET"
	ServeWrite = "serve-config POST"
)

// localAPIPassword is the password the fake LocalAPI asks for, as the
// Tailscale app for macOS does.
const localAPIPassword = "synthetic-localapi-password"

// Fake is one fake Tailscale: its LocalAPI, its state and the file that
// stands for its tailscale command.
type Fake struct {
	t *testing.T
	// Path is the empty file that stands for the tailscale command.
	Path     string
	localAPI *httptest.Server
	// mu guards state, the State as JSON, so that every reader gets its own
	// copy.
	mu    sync.Mutex
	state []byte
}

// fakes are the fakes of this process by Path, for Find.
var fakes sync.Map

// Find returns the Command of the fake at path, as tailscale.Find returns
// the real one, or tailscale.ErrNotInstalled when no fake of this process
// has that path. It never returns a Command that reaches the real Tailscale.
func Find(path string) (tailscale.Command, error) {
	if fake, ok := fakes.Load(path); ok {
		return fake.(*Fake).Command(), nil
	}
	return tailscale.Command{}, tailscale.ErrNotInstalled
}

// Command returns the command whose LocalAPI is the fake's.
func (fake *Fake) Command() tailscale.Command {
	address := fake.localAPI.Listener.Addr().String()
	return tailscale.Command{Path: fake.Path, LocalAPI: func(ctx context.Context) (net.Conn, string, error) {
		var notRunning bool
		if err := fake.update(func(state *State) bool { notRunning = state.NotRunning; return false }); err != nil {
			return nil, "", err
		}
		if notRunning {
			return nil, "", errors.New("fake tailscale: connection refused")
		}
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, "tcp", address)
		return conn, localAPIPassword, err
	}}
}

// New creates a fake that reports state. Its LocalAPI and the file that
// stands for its command belong to this test.
func New(t *testing.T, state State) *Fake {
	t.Helper()
	name := "tailscale"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	fake := &Fake{t: t, Path: path, state: content}
	fake.localAPI = httptest.NewServer(http.HandlerFunc(fake.answer))
	fakes.Store(path, fake)
	t.Cleanup(func() {
		fakes.Delete(path)
		fake.localAPI.Close()
	})
	return fake
}

// Update changes the fake's state.
func (fake *Fake) Update(change func(*State)) {
	fake.t.Helper()
	if err := fake.update(func(state *State) bool { change(state); return true }); err != nil {
		fake.t.Fatal(err)
	}
}

// State returns the fake's current state.
func (fake *Fake) State() State {
	fake.t.Helper()
	var current State
	if err := fake.update(func(state *State) bool { current = *state; return false }); err != nil {
		fake.t.Fatal(err)
	}
	return current
}

// Endpoint describes what Tailscale has now on HTTPS port for the fake's
// name, with target as OwnGit's expected proxy target.
func (fake *Fake) Endpoint(port int, target string) tailscale.Endpoint {
	fake.t.Helper()
	current := fake.State()
	name := ""
	if current.Status.Self != nil {
		name = strings.TrimSuffix(current.Status.Self.DNSName, ".")
	}
	return current.Serve.Endpoint(name, port, target)
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

// Calls returns the calls so far, each joined with spaces.
func (fake *Fake) Calls() []string {
	fake.t.Helper()
	var calls []string
	for _, call := range fake.State().Calls {
		calls = append(calls, strings.Join(call, " "))
	}
	return calls
}

// Writes returns the calls so far that change Tailscale, or would: every
// call other than a read.
func (fake *Fake) Writes() []string {
	var writes []string
	for _, call := range fake.Calls() {
		if call != StatusRead && call != ServeRead {
			writes = append(writes, call)
		}
	}
	return writes
}

// update runs change on a copy of the state and keeps that copy when
// change returns true.
func (fake *Fake) update(change func(*State) bool) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var state State
	if err := json.Unmarshal(fake.state, &state); err != nil {
		return err
	}
	if !change(&state) {
		return nil
	}
	content, err := json.Marshal(state)
	if err != nil {
		return err
	}
	fake.state = content
	return nil
}

// call runs one LocalAPI request: it counts the call as running, waits for
// the delay of a write or a read and for a held read to be released, then
// records the call and answers it from the state.
func (fake *Fake) call(name string, write, read bool, answer func(*State)) error {
	var started State
	err := fake.update(func(state *State) bool {
		state.Running++
		state.MaxRunning = max(state.MaxRunning, state.Running)
		started = *state
		return true
	})
	switch {
	case err != nil:
		return err
	case write && started.WriteDelay > 0:
		time.Sleep(time.Duration(started.WriteDelay) * time.Millisecond)
	case read && started.ReadDelay > 0:
		time.Sleep(time.Duration(started.ReadDelay) * time.Millisecond)
	}
	if read {
		if err := fake.awaitRelease(); err != nil {
			return err
		}
	}
	return fake.update(func(state *State) bool {
		state.Running--
		state.Calls = append(state.Calls, []string{name})
		answer(state)
		return true
	})
}

// awaitRelease returns once this read may answer: nothing holds reads, or a
// pass is left (HoldReads).
func (fake *Fake) awaitRelease() error {
	held := false
	for {
		released := false
		err := fake.update(func(state *State) bool {
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

// answer answers a LocalAPI request as Tailscale does. A status read, of
// the status without other devices, gets Status. A read of the serve-config
// resource gets the configuration and its version, the SHA-256 of the JSON,
// in the ETag header; a change replaces the configuration only when its
// If-Match header has the current version, and is otherwise answered 412
// without a change.
func (fake *Fake) answer(response http.ResponseWriter, request *http.Request) {
	if _, password, _ := request.BasicAuth(); password != localAPIPassword {
		http.Error(response, "bad password", http.StatusForbidden)
		return
	}
	if request.Host != "local-tailscaled.sock" {
		http.NotFound(response, request)
		return
	}
	switch {
	case request.URL.Path == "/localapi/v0/status" && request.Method == http.MethodGet && request.URL.RawQuery == "peers=false":
		fake.status(response)
	case request.URL.Path == "/localapi/v0/serve-config":
		fake.serveConfig(response, request)
	default:
		http.NotFound(response, request)
	}
}

// status answers a status read.
func (fake *Fake) status(response http.ResponseWriter) {
	var code int
	var answer []byte
	err := fake.call(StatusRead, false, true, func(state *State) {
		if state.StatusError != "" {
			code, answer = http.StatusInternalServerError, errorJSON(state.StatusError)
			return
		}
		code = http.StatusOK
		answer, _ = json.Marshal(state.Status)
	})
	if err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(code)
	response.Write(answer)
}

// serveConfig answers a request to the serve-config resource.
func (fake *Fake) serveConfig(response http.ResponseWriter, request *http.Request) {
	write := request.Method == http.MethodPost
	name := ServeRead
	if write {
		name = ServeWrite
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	var status int
	var answer []byte
	var version string
	err = fake.call(name, write, !write, func(state *State) {
		if write {
			status, answer = state.change(request.Header.Get("If-Match"), body)
			return
		}
		if state.Wrote && state.ServeReadErrorAfterWrite != "" {
			status, answer = http.StatusInternalServerError, errorJSON(state.ServeReadErrorAfterWrite)
			return
		}
		status, answer = http.StatusOK, state.serveJSON()
		if !state.Unversioned {
			version = versionOf(answer)
		}
	})
	if err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
		return
	}
	if version != "" {
		response.Header().Set("Etag", version)
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	response.Write(answer)
}

// change answers a Serve change with body made from version ifMatch.
func (state *State) change(ifMatch string, body []byte) (int, []byte) {
	if state.ChangedBeforeWrite != nil {
		state.Serve, state.ChangedBeforeWrite = *state.ChangedBeforeWrite, nil
	}
	switch {
	case state.WriteDenied:
		return http.StatusForbidden, []byte("serve config denied\n")
	case state.WriteError != "":
		return http.StatusInternalServerError, errorJSON("updating config: " + state.WriteError)
	case ifMatch != "" && ifMatch != versionOf(state.serveJSON()) && !state.Unversioned:
		return http.StatusPreconditionFailed, []byte("etag mismatch\n")
	}
	var fields map[string]json.RawMessage
	var config tailscale.ServeConfig
	if err := json.Unmarshal(body, &fields); err != nil {
		return http.StatusInternalServerError, errorJSON("decoding config: " + err.Error())
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return http.StatusInternalServerError, errorJSON("decoding config: " + err.Error())
	}
	if !state.IgnoreWrites {
		for _, known := range []string{"TCP", "Web", "AllowFunnel", "Foreground"} {
			delete(fields, known)
		}
		state.Serve, state.ServeExtra = config, fields
	}
	state.Wrote = true
	if state.WriteErrorAfterChange != "" {
		return http.StatusInternalServerError, errorJSON(state.WriteErrorAfterChange)
	}
	return http.StatusOK, nil
}

// serveJSON is the Serve configuration as Tailscale writes it.
func (state *State) serveJSON() []byte {
	var fields map[string]json.RawMessage
	content, _ := json.Marshal(state.Serve)
	_ = json.Unmarshal(content, &fields)
	for field, value := range state.ServeExtra {
		fields[field] = value
	}
	if len(fields) == 0 {
		return []byte("null")
	}
	content, _ = json.Marshal(fields)
	return content
}

func versionOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func errorJSON(text string) []byte {
	content, _ := json.Marshal(struct{ Error string }{text})
	return content
}
