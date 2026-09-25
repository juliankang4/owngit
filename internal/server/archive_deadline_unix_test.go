//go:build !windows

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/pullrequest"
)

// slowArchiveStart makes git archive wait before it writes anything. Every
// other Git command runs normally.
func slowArchiveStart(t *testing.T, fixture apiFixture, wait string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	noErr(t, err)
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\ncase \" $* \" in *\" archive \"*) sleep " + wait + ";; esac\nexec '" + realGit + "' \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	fixture.app.GitHTTP.Git.GitPath = wrapper
}

// Archives are Git transfers: the page time limit does not cut them, while
// pages keep it.
func TestArchiveOutlivesThePageDeadline(t *testing.T) {
	fixture := newAPIFixture(t, false)
	fixture.app.HTTPTimeout = time.Second
	slowArchiveStart(t, fixture, "2")
	server := serve(t, fixture.app.Handler())
	for _, test := range []struct{ target, format string }{
		{"/api/v1/repositories/project/archive?ref=main&format=zip", "zip"},
		{"/api/v1/repositories/project/archive?ref=main&format=tar.gz", "tar.gz"},
		{"/repositories/project/archive?ref=main&format=zip", "zip"},
	} {
		started := time.Now()
		response, body := getArchive(t, server.URL+test.target)
		if response.StatusCode != http.StatusOK || time.Since(started) < 2*time.Second || archiveNames(t, test.format, body) != "project-main/,project-main/file.txt" {
			t.Fatalf("%s: status=%d bytes=%d after %s", test.target, response.StatusCode, len(body), time.Since(started))
		}
	}

	for target, want := range map[string]time.Duration{
		"/repositories/project/archive":        fixture.app.GitHTTP.OperationTimeout + 2*replyReserve,
		"/api/v1/repositories/project/archive": fixture.app.GitHTTP.OperationTimeout + 2*replyReserve,
		"/repositories/project/code":           time.Second,
		"/repositories/project/archive/x":      time.Second,
		"/repositories/a/b/archive":            time.Second,
	} {
		if got, _ := fixture.app.requestTimeout(httptest.NewRequest(http.MethodGet, target, nil)); got != want {
			t.Errorf("%s: request timeout %s, want %s", target, got, want)
		}
	}
	if got, _ := fixture.app.requestTimeout(httptest.NewRequest(http.MethodPost, "/api/v1/repositories/project/archive", nil)); got != time.Second {
		t.Errorf("POST archive: request timeout %s", got)
	}
}

// A request that fails before the first archive byte answers an error status
// in its route's format, never an empty success.
func TestArchiveFailureBeforeTheFirstByteAnswersAnError(t *testing.T) {
	fixture := newAPIFixture(t, false)
	fixture.app.GitHTTP.OperationTimeout = time.Second
	server := serve(t, fixture.app.Handler())
	api := server.URL + "/api/v1/repositories/project/archive?ref=main&format=zip"
	browser := server.URL + "/repositories/project/archive?ref=main&format=zip"
	requireRefusal := func(when string) {
		t.Helper()
		response, body := getArchive(t, api)
		var envelope pullrequest.ErrorEnvelope
		if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") == "" || response.Header.Get("Content-Disposition") != "" ||
			json.Unmarshal(body, &envelope) != nil || envelope.Error.Code != "repository_unavailable" {
			t.Fatalf("%s, API: status=%d retry=%q body=%.200q", when, response.StatusCode, response.Header.Get("Retry-After"), body)
		}
		response, body = getArchive(t, browser)
		if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Content-Disposition") != "" ||
			!strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") || len(body) == 0 {
			t.Fatalf("%s, browser: status=%d type=%q bytes=%d", when, response.StatusCode, response.Header.Get("Content-Type"), len(body))
		}
	}

	// The time limit ends before Git wrote anything.
	slowArchiveStart(t, fixture, "3")
	requireRefusal("Git past the time limit")

	// A push holds the repository while the archive waits for it.
	lock := fixture.app.Repositories.Locks.For("project")
	lock.Lock()
	requireRefusal("repository locked")
	lock.Unlock()
}
