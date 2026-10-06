//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/checkworkflow"
)

// In a partial clone the committed check file may be missing, and Git then
// fetches it from the promisor remote. A remote that never answers must not hold
// the run: reading the committed file ends within its bound with a preparation
// error, and the fetch process Git started ends with it. The fake
// remote is an upload-pack command that records its process id and then waits
// on a pipe nobody opens.
func TestPartialCloneWithAHangingPromisorRemoteEndsWithAPreparationError(t *testing.T) {
	root := t.TempDir()
	git := func(directory string, arguments ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test", "-c", "protocol.file.allow=always"}, arguments...)...)
		command.Dir = directory
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, filepath.Dir(checkworkflow.Path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, checkworkflow.Path), []byte(`{"checks":[{"name":"unit","command":"true"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	git(source, "init", "-q")
	git(source, "config", "uploadpack.allowFilter", "true")
	git(source, "add", ".")
	git(source, "commit", "-q", "-m", "checks")
	clone := filepath.Join(root, "clone")
	git(root, "clone", "-q", "--no-checkout", "--filter=blob:none", "file://"+source, clone)
	pidFile := filepath.Join(root, "promisor.pid")
	fifo := filepath.Join(root, "never-opened")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "upload-pack")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho $$ > '"+pidFile+"'\nread line < '"+fifo+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(clone, "config", "remote.origin.uploadpack", script)

	previous := committedCheckReadBound
	committedCheckReadBound = 500 * time.Millisecond
	t.Cleanup(func() { committedCheckReadBound = previous })
	_, err := committedCheckDefinitions(context.Background(), clone, git(clone, "rev-parse", "HEAD"))
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != "revision_unavailable" {
		t.Fatalf("error=%v, want the revision_unavailable preparation error", err)
	}
	waitForFilterGone(t, waitForFilterStart(t, pidFile))
}
