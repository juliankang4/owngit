//go:build !windows

package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// endlessArchive makes git archive write zeros until it is stopped. Every
// other Git command runs normally.
func endlessArchive(t *testing.T, fixture apiFixture) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	noErr(t, err)
	wrapper := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\ncase \" $* \" in *\" archive \"*) exec cat /dev/zero;; esac\nexec '" + realGit + "' \"$@\"\n"
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	fixture.app.GitHTTP.Git.GitPath = wrapper
	fixture.app.GitHTTP.Git.TerminationGrace = 25 * time.Millisecond
}

// An archive download whose client stops reading is stopped at the idle
// limit on the browser and the API route, and the repository is free again.
func TestArchiveIdleLimitStopsAStalledDownload(t *testing.T) {
	const idle = 300 * time.Millisecond
	fixture := newAPIFixture(t, false)
	fixture.app.GitHTTP.IdleTimeout = idle
	endlessArchive(t, fixture)
	var serverLog lockedLog
	previousLog, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&serverLog)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousLog)
		log.SetFlags(previousFlags)
	})
	server := serve(t, fixture.app.Handler())
	address := server.Listener.Addr().String()
	for _, target := range []string{
		"/repositories/project/archive?ref=main&format=zip",
		"/api/v1/repositories/project/archive?ref=main&format=zip",
	} {
		connection, err := net.Dial("tcp", address)
		noErr(t, err)
		started := time.Now()
		_, err = fmt.Fprintf(connection, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, address)
		noErr(t, err)
		deadline := time.Now().Add(5 * time.Second)
		for fixture.app.GitHTTP.Active() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		for fixture.app.GitHTTP.Active() != 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		elapsed := time.Since(started)
		_ = connection.Close()
		if fixture.app.GitHTTP.Active() != 0 || elapsed < idle {
			t.Fatalf("%s: stalled download ended after %s, active=%d", target, elapsed, fixture.app.GitHTTP.Active())
		}
		lockContext, cancel := context.WithTimeout(context.Background(), time.Second)
		lock := fixture.app.Repositories.Locks.For("project")
		noErr(t, lock.LockContext(lockContext))
		lock.Unlock()
		cancel()
	}
	want := `Git archive request for repository "project" failed: no data moved for 300ms (Git transfer idle limit)`
	if got := strings.Count(serverLog.String(), want); got != 2 {
		t.Fatalf("log names the idle limit %d times, want 2: %s", got, serverLog.String())
	}
}

// Git that writes nothing for longer than the idle limit before the first
// archive byte still completes the download on both routes.
func TestArchiveIdleLimitSparesAQuietStart(t *testing.T) {
	fixture := newAPIFixture(t, false)
	fixture.app.GitHTTP.IdleTimeout = 300 * time.Millisecond
	slowArchiveStart(t, fixture, "1")
	server := serve(t, fixture.app.Handler())
	for _, target := range []string{
		"/repositories/project/archive?ref=main&format=zip",
		"/api/v1/repositories/project/archive?ref=main&format=tar.gz",
	} {
		format := "zip"
		if strings.HasSuffix(target, "tar.gz") {
			format = "tar.gz"
		}
		started := time.Now()
		response, body := getArchive(t, server.URL+target)
		if response.StatusCode != http.StatusOK || time.Since(started) < time.Second || archiveNames(t, format, body) != "project-main/,project-main/file.txt" {
			t.Fatalf("%s: status=%d bytes=%d after %s", target, response.StatusCode, len(body), time.Since(started))
		}
	}
}
