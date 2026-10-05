package pullrequest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
)

// TestReconcileAllReadsPullRequestRefsOnce proves that startup reconciliation
// lists each repository's pull request refs once instead of starting a Git
// process per recorded ref, and still repairs a missing one.
func TestReconcileAllReadsPullRequestRefsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command-recording wrapper is a Unix test fixture")
	}
	fixture, number, newSource, targetOID := newMovedHeadFixture(t)
	if _, _, err := fixture.service.ObserveCurrentRevisionsAfter(fixture.ctx, fixture.repositoryID, 0, 10); err != nil {
		t.Fatal(err)
	}
	revisions, err := fixture.store.PullRequestRevisions(fixture.ctx, fixture.repositoryID)
	noErr(t, err)
	if len(revisions) != 2 {
		t.Fatalf("fixture recorded %d revisions, want 2", len(revisions))
	}

	gitPath, err := exec.LookPath("git")
	noErr(t, err)
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace")
	wrapper := filepath.Join(dir, "git")
	noErr(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+shellQuote(tracePath)+"\nexec "+shellQuote(gitPath)+" \"$@\"\n"), 0o700))
	runner, err := gitexec.New(wrapper, filepath.Join(dir, "runtime"))
	noErr(t, err)
	fixture.manager.Git = runner
	commands := func() []string {
		t.Helper()
		trace, err := os.ReadFile(tracePath)
		noErr(t, err)
		noErr(t, os.WriteFile(tracePath, nil, 0o600))
		var subcommands []string
		for _, line := range strings.Split(strings.TrimSpace(string(trace)), "\n") {
			if fields := strings.Fields(line); len(fields) > 2 {
				subcommands = append(subcommands, fields[2])
			}
		}
		return subcommands
	}

	noErr(t, fixture.service.ReconcileAll(fixture.ctx))
	if got := commands(); len(got) != 1 || got[0] != "for-each-ref" {
		t.Fatalf("reconciliation of intact refs ran %q, want one for-each-ref", got)
	}

	sourceRef, _ := RevisionRefNames(number, newSource, targetOID)
	fixture.git("--git-dir", fixture.remote, "update-ref", "-d", sourceRef)
	noErr(t, fixture.service.ReconcileAll(fixture.ctx))
	if fixture.ref(sourceRef) != newSource {
		t.Fatal("reconciliation did not repair a missing revision ref")
	}
	for _, subcommand := range commands() {
		if subcommand == "rev-parse" {
			t.Fatal("reconciliation read a ref with its own Git process")
		}
	}
}

// TestReconcileAllNamesATimeoutCause proves that a Git read that exceeds its
// bound reports the timeout in the message, not only that refs "could not be
// read", and stays a failure.
func TestReconcileAllNamesATimeoutCause(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the slow Git wrapper is a Unix test fixture")
	}
	fixture, _, _, _ := newMovedHeadFixture(t)
	_, _, err := fixture.service.ObserveCurrentRevisionsAfter(fixture.ctx, fixture.repositoryID, 0, 10)
	noErr(t, err)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "git")
	noErr(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700))
	runner, err := gitexec.New(wrapper, filepath.Join(dir, "runtime"))
	noErr(t, err)
	runner.Timeout = 200 * time.Millisecond
	fixture.manager.Git = runner

	err = fixture.service.ReconcileAll(fixture.ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("ReconcileAll error = %v, want a failure that names the timeout", err)
	}
	for _, row := range []struct{ code, message, want string }{
		{"repository_busy", "Another Git operation is using the repository.", "Another Git operation is using the repository."},
		{"repository_unavailable", "The repository storage is unavailable.", "The repository storage is unavailable: it did not finish in time."},
		{"state_unavailable", "Pull request metadata could not be read.", "Pull request metadata could not be read: it did not finish in time."},
	} {
		got := (&Problem{Code: row.code, Message: row.message, Cause: context.DeadlineExceeded}).Error()
		if got != row.want {
			t.Fatalf("%s deadline error = %q, want %q", row.code, got, row.want)
		}
	}
}
