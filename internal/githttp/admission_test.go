package githttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// One repository keeps all its slots, a repository with no transfer running
// always finds the extra slot, and a request that finds no slot waits only for
// the queue wait (QA-019).
func TestAdmissionKeepsASlotForIdleRepositoriesAndBoundsTheWait(t *testing.T) {
	slots := newAdmission(4)
	ctx := context.Background()
	var releases []func()
	take := func(id string) {
		t.Helper()
		release, err := slots.acquire(ctx, id, 0)
		noErr(t, err, "acquire for "+id)
		releases = append(releases, release)
	}
	for range 4 {
		take("big")
	}
	started := time.Now()
	if _, err := slots.acquire(ctx, "big", 50*time.Millisecond); !errors.Is(err, errBusy) {
		t.Fatalf("fifth transfer of one repository: %v, want busy", err)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("busy answer took %s", waited)
	}
	take("small")
	if _, err := slots.acquire(ctx, "third", 50*time.Millisecond); !errors.Is(err, errBusy) {
		t.Fatalf("request beyond the total: %v, want busy", err)
	}
	got := make(chan error, 1)
	go func() {
		release, err := slots.acquire(ctx, "third", 5*time.Second)
		if err == nil {
			release()
		}
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
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
	if _, err := slots.acquire(ctx, "d", 20*time.Millisecond); !errors.Is(err, errBusy) {
		t.Fatalf("request beyond the extra slot: %v, want busy", err)
	}
	if _, err := slots.acquire(ctx, "a", 20*time.Millisecond); !errors.Is(err, errBusy) {
		t.Fatalf("a busy repository took the extra slot: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := slots.acquire(cancelled, "d", time.Minute); !errors.Is(err, context.Canceled) {
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
	handler, err := New(runner, manager, "", 1)
	noErr(t, err)
	backend, err := os.Executable()
	noErr(t, err)
	handler.BackendPath = backend
	handler.Authorize = func(*http.Request) bool { return true }
	handler.QueueWait = 100 * time.Millisecond
	logs := captureLog(t)
	// With one slot per repository, two other repositories fill both slots.
	var releases []func()
	for _, id := range []string{"other", "third"} {
		release, err := handler.slots.acquire(context.Background(), id, 0)
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
