package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
)

// servedInstance is one serveWithContext process running inside the test.
type servedInstance struct {
	t      *testing.T
	url    string
	cancel context.CancelFunc
	done   chan struct{} // closed when serve has returned err
	err    error
	stops  sync.Once
	mu     sync.Mutex
	logs   []string
}

var listeningLine = regexp.MustCompile(`OwnGit listening on (\S+)`)

func startServed(t *testing.T, stateDir string, extra ...string) *servedInstance {
	t.Helper()
	return startServedWith(t, append([]string{"--state-dir", stateDir, "--listen", "127.0.0.1:0", "--no-open"}, extra...))
}

// startServedWith starts serve with exactly these arguments. The test
// stops it at the latest when it ends, also when the start fails, so serve
// never outlives the test and its folders.
func startServedWith(t *testing.T, arguments []string) *servedInstance {
	t.Helper()
	instance := &servedInstance{t: t, done: make(chan struct{})}
	listening := make(chan string, 1)
	logf := func(format string, arguments ...any) {
		line := fmt.Sprintf(format, arguments...)
		instance.mu.Lock()
		instance.logs = append(instance.logs, line)
		instance.mu.Unlock()
		if match := listeningLine.FindStringSubmatch(line); match != nil {
			select {
			case listening <- "http://" + match[1]:
			default:
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	instance.cancel = cancel
	t.Cleanup(instance.stop)
	go func() {
		defer close(instance.done)
		instance.err = serveWithContext(ctx, arguments, func(string) error { return nil }, logf)
	}()
	// Wait for the listening line, however slow a busy machine makes the
	// start. A serve that returns instead failed to start, and says why; the
	// bound only keeps a hung start from reaching the test binary's timeout.
	select {
	case instance.url = <-listening:
	case <-instance.done:
		t.Fatalf("serve returned before it listened: %v\n%s", instance.err, instance.log())
	case <-time.After(2 * time.Minute):
		t.Fatalf("serve did not listen within 2 minutes: %s", instance.log())
	}
	return instance
}

func (instance *servedInstance) log() string {
	instance.mu.Lock()
	defer instance.mu.Unlock()
	return strings.Join(instance.logs, "\n")
}

// stop ends serve and waits for it. Only the first call does so. The
// error of a serve that never listened was reported by its start.
func (instance *servedInstance) stop() {
	instance.t.Helper()
	instance.stops.Do(func() {
		instance.cancel()
		select {
		case <-instance.done:
		case <-time.After(90 * time.Second):
			instance.t.Fatal("serve did not stop")
		}
		if instance.err != nil && instance.url != "" {
			instance.t.Fatalf("serve returned %v\n%s", instance.err, instance.log())
		}
	})
}

// completeSetupOverHTTP drives the owner setup forms of a running serve.
func completeSetupOverHTTP(t *testing.T, store *state.Store, base, repositoryRoot, adminPassword string) {
	t.Helper()
	const token = "lifetime-owner-setup-token"
	noErr(t, store.PutBootstrap(context.Background(), token, time.Now().Add(time.Hour)))
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, values url.Values) int {
		request, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(values.Encode()))
		noErr(t, err)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("Origin", base)
		response, err := client.Do(request)
		noErr(t, err)
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return response.StatusCode
	}
	response, err := client.Get(base + "/setup")
	noErr(t, err)
	response.Body.Close()
	cookie := func(name string) string {
		parsed, _ := url.Parse(base)
		for _, item := range jar.Cookies(parsed) {
			if item.Name == name {
				return item.Value
			}
		}
		t.Fatalf("cookie %s is missing", name)
		return ""
	}
	if status := post("/setup/redeem", url.Values{"csrf": {cookie("owngit_preauth")}, "token": {token}}); status != http.StatusSeeOther {
		t.Fatalf("redeem status=%d", status)
	}
	session, ok, err := store.Session(context.Background(), cookie("owngit_setup"), "setup", time.Now())
	if err != nil || !ok {
		t.Fatalf("setup session ok=%v err=%v", ok, err)
	}
	if status := post("/setup", url.Values{
		"csrf": {session.CSRF}, "storage_path": {repositoryRoot}, "access_mode": {"open"},
		"admin_password": {adminPassword}, "insecure_ack": {"on"},
	}); status != http.StatusSeeOther {
		t.Fatalf("setup status=%d", status)
	}
}

func importRuntimeStatus(t *testing.T, base, repositoryID, adminPassword string) (bool, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, base+"/api/v1/repositories/"+repositoryID+"/import", nil)
	noErr(t, err)
	request.SetBasicAuth("admin", adminPassword)
	response, err := http.DefaultClient.Do(request)
	noErr(t, err)
	defer response.Body.Close()
	var envelope struct {
		Status struct {
			Runtime struct {
				SchedulerRunning bool   `json:"scheduler_running"`
				Code             string `json:"code"`
			} `json:"runtime"`
		} `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("import status code=%d err=%v", response.StatusCode, err)
	}
	return envelope.Status.Runtime.SchedulerRunning, envelope.Status.Runtime.Code
}

// First-run setup completed inside a running serve starts the import runtime
// then: the status surface reports a running scheduler, and a schedule that
// became due while setup was pending is claimed without a restart.
func TestSchedulerStartsWhenSetupCompletesWhileServing(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	root := filepath.Join(base, "repositories")
	noErr(t, os.MkdirAll(root, 0o700))
	served := startServed(t, stateDir)
	ctx := context.Background()
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	defer store.Close()
	// A started scheduler looks for due schedules at once, so a schedule
	// that is due before setup shows that setup started it, without waiting
	// for a later tick.
	if output, err := exec.Command("git", "init", "--bare", "-q", filepath.Join(root, "demo.git")).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	now := time.Now().UTC()
	noErr(t, store.AddRepository(ctx, state.Repository{ID: "demo", Name: "demo", CreatedAt: now}))
	// An unreachable source keeps the scheduled run short and offline.
	if _, err := store.ConfigureImportSource(ctx, state.ImportSourceInput{RepositoryID: "demo", URL: "https://127.0.0.1:9/demo.git", Mode: "standalone", Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetImportSchedule(ctx, "demo", true, 60*time.Second, now); err != nil {
		t.Fatal(err)
	}
	const adminPassword = "lifetime-admin-password"
	completeSetupOverHTTP(t, store, served.url, root, adminPassword)

	settings, err := store.Settings(ctx)
	if err != nil || !settings.Initialized {
		t.Fatalf("setup did not complete settings=%+v err=%v", settings, err)
	}
	if running, code := importRuntimeStatus(t, served.url, "demo", adminPassword); !running || code != "" {
		t.Fatalf("scheduler after first-run setup running=%v code=%q\n%s", running, code, served.log())
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		schedule, _, err := store.ImportSchedule(ctx, "demo")
		noErr(t, err)
		if schedule.LastStartedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("schedule enabled after first-run setup was never claimed\n%s", served.log())
		}
		time.Sleep(200 * time.Millisecond)
	}
}
