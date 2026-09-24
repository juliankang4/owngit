package releasecheck

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub answers like the releases/latest endpoint with a chosen body.
func fakeGitHub(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var hits atomic.Int32
	var agent atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		agent.Store(request.Header.Get("User-Agent"))
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &hits, &agent
}

func tagBody(tag string) string {
	return fmt.Sprintf(`{"tag_name":%q,"draft":false,"prerelease":false,"html_url":"https://example.invalid/phish"}`, tag)
}

func TestVersionComparisonAndStrictTags(t *testing.T) {
	cases := []struct {
		tag   string
		newer bool
		fails bool
	}{
		{tag: "v1.0.3", newer: true},
		{tag: "v1.1.0", newer: true},
		{tag: "v2.0.0", newer: true},
		{tag: "v1.0.10", newer: true},
		{tag: "v1.0.2"},
		{tag: "v1.0.1"},
		{tag: "v0.9.9"},
		{tag: "1.0.3", fails: true},
		{tag: "v1.0", fails: true},
		{tag: "v1.0.3.1", fails: true},
		{tag: "v1.0.3-rc.1", fails: true},
		{tag: "v1.0.3-beta", fails: true},
		{tag: "v1.0.3+build", fails: true},
		{tag: "v01.0.3", fails: true},
		{tag: "v1.0.3 ", fails: true},
		{tag: "V1.0.3", fails: true},
		{tag: "v1234567890.0.0", fails: true},
		{tag: "", fails: true},
	}
	for _, tc := range cases {
		server, _, _ := fakeGitHub(t, http.StatusOK, tagBody(tc.tag))
		checker := &Checker{Current: "1.0.2", URL: server.URL}
		err := checker.Check(context.Background())
		if (err != nil) != tc.fails {
			t.Errorf("%q: error %v, want failure %v", tc.tag, err, tc.fails)
			continue
		}
		release, newer := checker.Newer()
		if newer != tc.newer {
			t.Errorf("%q: newer %v, want %v", tc.tag, newer, tc.newer)
		}
		if newer && (release.Version != tc.tag[1:] || release.NotesURL != "https://github.com/juliankang4/owngit/releases/tag/"+tc.tag) {
			t.Errorf("%q: release %+v; the notes link must be built from the tag, not the answer", tc.tag, release)
		}
	}
}

func TestRequestNamesOwnGitAndItsVersion(t *testing.T) {
	server, _, agent := fakeGitHub(t, http.StatusOK, tagBody("v1.0.2"))
	checker := &Checker{Current: "1.0.2", URL: server.URL}
	if err := checker.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := agent.Load().(string); !strings.HasPrefix(got, "OwnGit/1.0.2 ") {
		t.Fatalf("User-Agent %q does not name OwnGit and its version", got)
	}
}

func TestFailuresAreSilentAndLoggedOncePerStreak(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	logf := func(format string, arguments ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, arguments...))
		mu.Unlock()
	}
	status, body := http.StatusOK, tagBody("v1.0.3")
	var bodyMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		bodyMu.Lock()
		defer bodyMu.Unlock()
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	defer server.Close()
	answer := func(code int, text string) {
		bodyMu.Lock()
		status, body = code, text
		bodyMu.Unlock()
	}
	checker := &Checker{Current: "1.0.2", URL: server.URL, Logf: logf}

	if err := checker.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	failures := []struct {
		code int
		body string
	}{
		{http.StatusForbidden, `{"message":"API rate limit exceeded"}`},
		{http.StatusNotFound, `{}`},
		{http.StatusMovedPermanently, ``},
		{http.StatusOK, `not json`},
		{http.StatusOK, `{"tag_name":"v9.9.9","prerelease":true}`},
		{http.StatusOK, `{"tag_name":"v9.9.9","draft":true}`},
		{http.StatusOK, `{"tag_name":"latest"}`},
	}
	for _, failure := range failures {
		answer(failure.code, failure.body)
		if err := checker.Check(context.Background()); err == nil {
			t.Errorf("status %d body %q was accepted", failure.code, failure.body)
		}
		// A failed check keeps the earlier answer.
		if release, newer := checker.Newer(); !newer || release.Version != "1.0.3" {
			t.Errorf("status %d: earlier result lost: %+v %v", failure.code, release, newer)
		}
	}
	mu.Lock()
	if len(lines) != 1 {
		t.Fatalf("a failure streak logged %d lines, want 1: %q", len(lines), lines)
	}
	mu.Unlock()

	// Success ends the streak, so the next failure is logged again.
	answer(http.StatusOK, tagBody("v1.0.2"))
	if err := checker.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, newer := checker.Newer(); newer {
		t.Fatal("an equal version still produces a notice")
	}
	answer(http.StatusInternalServerError, ``)
	_ = checker.Check(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 2 {
		t.Fatalf("a new failure streak logged %d lines in total, want 2", len(lines))
	}
}

func TestUnreachableServerFails(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	endpoint := server.URL
	server.Close()
	checker := &Checker{Current: "1.0.2", URL: endpoint}
	if err := checker.Check(context.Background()); err == nil {
		t.Fatal("an unreachable endpoint was accepted")
	}
	if _, newer := checker.Newer(); newer {
		t.Fatal("a failure produced a notice")
	}
}

func TestResponseSizeIsLimited(t *testing.T) {
	padding := strings.Repeat("x", maxResponseBytes)
	server, _, _ := fakeGitHub(t, http.StatusOK, `{"tag_name":"v1.0.3","body":"`+padding+`"}`)
	checker := &Checker{Current: "1.0.2", URL: server.URL}
	if err := checker.Check(context.Background()); err != errTooLarge {
		t.Fatalf("oversized answer: %v, want %v", err, errTooLarge)
	}
	if _, newer := checker.Newer(); newer {
		t.Fatal("an oversized answer produced a notice")
	}

	// An answer within the limit still works.
	fits := strings.Repeat("x", maxResponseBytes-100)
	server, _, _ = fakeGitHub(t, http.StatusOK, `{"tag_name":"v1.0.3","body":"`+fits+`"}`)
	checker = &Checker{Current: "1.0.2", URL: server.URL}
	if err := checker.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSlowServerTimesOutWithTheContext(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	checker := &Checker{Current: "1.0.2", URL: server.URL}
	if err := checker.Check(ctx); err == nil {
		t.Fatal("a stalled answer was accepted")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the check waited %v for a stalled server", elapsed)
	}
}

func TestUnparsableRunningVersionNeverNotifies(t *testing.T) {
	server, hits, _ := fakeGitHub(t, http.StatusOK, tagBody("v9.0.0"))
	checker := &Checker{Current: "dev", URL: server.URL}
	if err := checker.Check(context.Background()); err == nil {
		t.Fatal("an unparsable running version was accepted")
	}
	if _, newer := checker.Newer(); newer || hits.Load() != 0 {
		t.Fatalf("newer=%v requests=%d", newer, hits.Load())
	}
}

func TestRunHonoursTheSavedSetting(t *testing.T) {
	server, hits, _ := fakeGitHub(t, http.StatusOK, tagBody("v1.0.3"))
	var enabled atomic.Bool
	checker := &Checker{
		Current: "1.0.2", URL: server.URL, InitialDelay: time.Hour, Interval: time.Hour,
		Enabled: func(context.Context) (bool, error) { return enabled.Load(), nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { checker.Run(ctx); close(done) }()
	defer func() {
		cancel()
		<-done
	}()

	// Off: a wake makes no request.
	checker.Wake()
	time.Sleep(100 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("a disabled checker made %d requests", hits.Load())
	}

	// On: a wake checks soon, without waiting for the interval.
	enabled.Store(true)
	checker.Wake()
	waitFor(t, func() bool { _, newer := checker.Newer(); return newer })
	if hits.Load() != 1 {
		t.Fatalf("requests=%d, want 1", hits.Load())
	}

	// Off again: the next wake forgets the result and makes no request.
	enabled.Store(false)
	checker.Wake()
	waitFor(t, func() bool { _, newer := checker.Newer(); return !newer })
	if hits.Load() != 1 {
		t.Fatalf("requests=%d after turning off, want 1", hits.Load())
	}
}

func TestRunChecksAfterTheInitialDelay(t *testing.T) {
	server, hits, _ := fakeGitHub(t, http.StatusOK, tagBody("v1.0.3"))
	checker := &Checker{Current: "1.0.2", URL: server.URL, InitialDelay: 10 * time.Millisecond, Interval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { checker.Run(ctx); close(done) }()
	waitFor(t, func() bool { return hits.Load() == 1 })
	cancel()
	<-done
	if release, newer := checker.Newer(); !newer || release.Version != "1.0.3" {
		t.Fatalf("release %+v newer %v", release, newer)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
