package githttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"owngit/internal/state"
)

type admissionWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *admissionWaitContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

// One repository keeps all its slots, a repository with no transfer running
// always finds the extra slot, and a request that finds no slot waits only for
// the queue wait.
func TestAdmissionKeepsASlotForIdleRepositoriesAndBoundsTheWait(t *testing.T) {
	slots := newAdmission()
	ctx := context.Background()
	limits := func(wait time.Duration) Limits { return Limits{PerRepository: 4, ExtraSlots: 1, QueueWait: wait} }
	var releases []func()
	take := func(id string) {
		t.Helper()
		release, err := slots.acquire(ctx, id, limits(0))
		noErr(t, err, "acquire for "+id)
		releases = append(releases, release)
	}
	for range 4 {
		take("big")
	}
	started := time.Now()
	if _, err := slots.acquire(ctx, "big", limits(50*time.Millisecond)); !errors.Is(err, errBusy) {
		t.Fatalf("fifth transfer of one repository: %v, want busy", err)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("busy answer took %s", waited)
	}
	take("small")
	if _, err := slots.acquire(ctx, "third", limits(50*time.Millisecond)); !errors.Is(err, errBusy) {
		t.Fatalf("request beyond the total: %v, want busy", err)
	}
	got := make(chan error, 1)
	waiting := &admissionWaitContext{Context: ctx, waiting: make(chan struct{})}
	go func() {
		release, err := slots.acquire(waiting, "third", limits(5*time.Second))
		if err == nil {
			release()
		}
		got <- err
	}()
	select {
	case <-waiting.waiting:
	case err := <-got:
		t.Fatalf("request returned before waiting for a slot: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("request never waited for a slot")
	}
	releases[len(releases)-1]() // "small" leaves
	releases = releases[:len(releases)-1]
	select {
	case err := <-got:
		noErr(t, err, "a waiting request did not get the released slot")
	case <-time.After(2 * time.Second):
		t.Fatal("a waiting request was not woken by a released slot")
	}
	for _, release := range releases {
		release()
	}
	releases = nil

	// Two busy repositories share the four slots; a third still gets the
	// extra slot, a fourth waits.
	take("a")
	take("a")
	take("b")
	take("b")
	take("c")
	if _, err := slots.acquire(ctx, "d", limits(20*time.Millisecond)); !errors.Is(err, errBusy) {
		t.Fatalf("request beyond the extra slot: %v, want busy", err)
	}
	if _, err := slots.acquire(ctx, "a", limits(20*time.Millisecond)); !errors.Is(err, errBusy) {
		t.Fatalf("a busy repository took the extra slot: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := slots.acquire(cancelled, "d", limits(time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	for _, release := range releases {
		release()
	}
	if slots.active != 0 || len(slots.byRepository) != 0 {
		t.Fatalf("slots left after release: active=%d by repository=%v", slots.active, slots.byRepository)
	}
}

func TestBusyGitRequestGets503WithRetryAfter(t *testing.T) {
	manager, runner := newHTTPTestRepository(t)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	backend, err := os.Executable()
	noErr(t, err)
	handler.BackendPath = backend
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	limits := useLimits(t, handler, func(limits *Limits) {
		limits.PerRepository, limits.ExtraSlots, limits.QueueWait = 1, 1, 100*time.Millisecond
	})
	logs := captureLog(t)
	// With one slot per repository, two other repositories fill both slots.
	var releases []func()
	for _, id := range []string{"other", "third"} {
		release, err := handler.slots.acquire(context.Background(), id, *limits)
		noErr(t, err)
		releases = append(releases, release)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/git/sample.git/info/refs?service=git-upload-pack")
	noErr(t, err)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") == "" || !strings.Contains(string(body), "busy") {
		t.Fatalf("busy request status=%d Retry-After=%q body=%q", response.StatusCode, response.Header.Get("Retry-After"), body)
	}
	if !strings.Contains(logs.String(), `repository "sample" failed: no Git transfer slot became free within 100ms`) {
		t.Fatalf("busy refusal was not logged: %q", logs.String())
	}
	for _, release := range releases {
		release()
	}
	if active := handler.Active(); active != 0 {
		t.Fatalf("active=%d after a refused request", active)
	}
}

// Lowering the limits never stops a transfer that holds a slot: a request
// under the new limits waits until enough transfers have ended. Raising
// the extra slots lets more repositories with no transfer running in.
func TestAdmissionFollowsChangedLimitsWithoutStoppingTransfers(t *testing.T) {
	slots := newAdmission()
	ctx := context.Background()
	wide := Limits{PerRepository: 4, ExtraSlots: 1, QueueWait: time.Second}
	var running []func()
	for range 4 {
		release, err := slots.acquire(ctx, "a", wide)
		noErr(t, err)
		running = append(running, release)
	}
	narrow := Limits{PerRepository: 2, ExtraSlots: 4, QueueWait: 5 * time.Second}
	got := make(chan error, 1)
	go func() {
		release, err := slots.acquire(ctx, "a", narrow)
		if err == nil {
			release()
		}
		got <- err
	}()
	var others []func()
	for _, id := range []string{"b", "c"} {
		release, err := slots.acquire(ctx, id, narrow)
		noErr(t, err, "a repository with no transfer under more extra slots: "+id)
		others = append(others, release)
	}
	if _, err := slots.acquire(ctx, "d", Limits{PerRepository: 2, ExtraSlots: 4, QueueWait: 20 * time.Millisecond}); !errors.Is(err, errBusy) {
		t.Fatalf("an idle repository beyond both numbers added: %v, want busy", err)
	}
	if slots.active != 6 {
		t.Fatalf("active=%d, want the four running transfers kept and two more", slots.active)
	}
	for _, release := range others {
		release()
	}
	running[0]()
	running[1]()
	select {
	case err := <-got:
		t.Fatalf("admitted with three transfers of a running under a limit of two: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	running[2]()
	select {
	case err := <-got:
		noErr(t, err, "waiting request under the lower limit")
	case <-time.After(2 * time.Second):
		t.Fatal("a waiting request was not admitted once the repository went below the new limit")
	}
	running[3]()
}

// The saved transfer limits reach every clone: with one slot per
// repository and no extra slot, a clone of another repository waits for
// the busy one and is refused after the queue wait; with one extra slot it
// runs at once.
func TestSavedTransferLimitsDecideWhenACloneRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 5 second transfer queue wait")
	}
	manager, runner := newHTTPTestRepository(t)
	ctx := context.Background()
	_, err := manager.Create(ctx, "other", "")
	noErr(t, err)
	handler, err := New(runner, manager, "")
	noErr(t, err)
	handler.Authorize = func(*http.Request) (bool, error) { return true, nil }
	server := httptest.NewServer(handler)
	defer server.Close()
	save := func(extra int) Limits {
		t.Helper()
		limits := state.DefaultGitTransferLimits
		limits.PerRepository, limits.ExtraSlots, limits.QueueWait = 1, extra, state.MinimumTransferQueue
		noErr(t, manager.Store.SavePolicies(ctx, state.PolicyChange{GitTransfer: &limits}))
		saved, err := handler.Limits(ctx)
		noErr(t, err)
		return saved
	}
	release, err := handler.slots.acquire(ctx, "sample", save(0))
	noErr(t, err)
	defer release()
	started := time.Now()
	if output, err := httpGitCombined("", "clone", "-q", server.URL+"/git/other.git", filepath.Join(t.TempDir(), "refused")); err == nil {
		t.Fatalf("a clone ran with every slot taken: %s", output)
	}
	if waited := time.Since(started); waited < state.MinimumTransferQueue {
		t.Fatalf("the clone was refused after %s, before the queue wait", waited)
	}
	save(1)
	runHTTPGit(t, "", "clone", "-q", server.URL+"/git/other.git", filepath.Join(t.TempDir(), "extra"))
}
