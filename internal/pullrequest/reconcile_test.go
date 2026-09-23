package pullrequest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
