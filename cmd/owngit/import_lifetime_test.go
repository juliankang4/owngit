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
	result chan error
	mu     sync.Mutex
	logs   []string
}

var listeningLine = regexp.MustCompile(`OwnGit listening on (\S+)`)

func startServed(t *testing.T, stateDir string, extra ...string) *servedInstance {
	t.Helper()
	instance := &servedInstance{t: t, result: make(chan error, 1)}
	logf := func(format string, arguments ...any) {
		instance.mu.Lock()
		instance.logs = append(instance.logs, fmt.Sprintf(format, arguments...))
		instance.mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	instance.cancel = cancel
	go func() {
		arguments := append([]string{"--state-dir", stateDir, "--listen", "127.0.0.1:0", "--no-open"}, extra...)
		instance.result <- serveWithContext(ctx, arguments, func(string) error { return nil }, logf)
	}()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if match := listeningLine.FindStringSubmatch(instance.log()); match != nil {
			instance.url = "http://" + match[1]
			return instance
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	t.Fatalf("serve did not listen: %s", instance.log())
	return nil
}

func (instance *servedInstance) log() string {
	instance.mu.Lock()
	defer instance.mu.Unlock()
	return strings.Join(instance.logs, "\n")
}

func (instance *servedInstance) stop() {
	instance.t.Helper()
	instance.cancel()
	select {
	case err := <-instance.result:
		if err != nil {
			instance.t.Fatalf("serve returned %v\n%s", err, instance.log())
		}
	case <-time.After(90 * time.Second):
		instance.t.Fatal("serve did not stop")
	}
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
// then: the status surface reports a running scheduler, and a schedule
// enabled afterwards is claimed without a restart.
func TestSchedulerStartsWhenSetupCompletesWhileServing(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for one 30 second scheduler tick")
	}
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	root := filepath.Join(base, "repositories")
	noErr(t, os.MkdirAll(root, 0o700))
	served := startServed(t, stateDir)
	defer served.stop()
	ctx := context.Background()
	store, err := state.Open(ctx, stateDir)
	noErr(t, err)
	defer store.Close()
	const adminPassword = "lifetime-admin-password"
	completeSetupOverHTTP(t, store, served.url, root, adminPassword)

	settings, err := store.Settings(ctx)
	if err != nil || !settings.Initialized {
		t.Fatalf("setup did not complete settings=%+v err=%v", settings, err)
	}
	if output, err := exec.Command("git", "init", "--bare", "-q", filepath.Join(settings.RepositoryRoot, "demo.git")).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	now := time.Now().UTC()
	noErr(t, store.AddRepository(ctx, state.Repository{ID: "demo", Name: "demo", CreatedAt: now}))
	// An unreachable source keeps the scheduled run short and offline.
	if _, err := store.ConfigureImportSource(ctx, state.ImportSourceInput{RepositoryID: "demo", URL: "https://127.0.0.1:9/demo.git", Mode: "standalone", Now: now}); err != nil {
		t.Fatal(err)
	}
	if running, code := importRuntimeStatus(t, served.url, "demo", adminPassword); !running || code != "" {
		t.Fatalf("scheduler after first-run setup running=%v code=%q\n%s", running, code, served.log())
	}
	if _, err := store.SetImportSchedule(ctx, "demo", true, 60*time.Second, now); err != nil {
		t.Fatal(err)
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
