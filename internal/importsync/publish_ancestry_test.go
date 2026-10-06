package importsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"owngit/internal/state"
)

// failGit points the fixture's Git at a POSIX wrapper that answers when any
// argument equals trigger with the given shell body, and runs Git otherwise.
// The returned function restores the real Git.
func failGit(t *testing.T, f *fixture, name, trigger, body string) func() {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the execution fault uses a POSIX Git wrapper")
	}
	original := f.manager.Git.GitPath
	wrapper := filepath.Join(f.root, name)
	script := fmt.Sprintf("#!/bin/sh\nfor arg do\n if [ \"$arg\" = %s ]; then\n  %s\n fi\ndone\nexec '%s' \"$@\"\n", trigger, body, original)
	noErr(t, os.WriteFile(wrapper, []byte(script), 0o700))
	f.manager.Git.GitPath = wrapper
	return func() { f.manager.Git.GitPath = original }
}

const failMergeBase = "echo 'fatal: synthetic object read failure' >&2; exit 128"

func TestAncestryExecutionFailureIsNotACompletedDivergence(t *testing.T) {
	f := newFixture(t)
	f.commit("first", "first\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	local := f.localWork("dev", "local\n")
	f.git(f.source, "fetch", f.destinationPath(), "refs/heads/dev")
	f.git(f.source, "checkout", "-B", "dev", "FETCH_HEAD")
	upstream := f.commit("upstream includes local", "upstream\n")
	require(t, f.git(f.source, "merge-base", "--is-ancestor", local, upstream) == "", "fixture is not a fast-forward")
	before, err := f.store.ImportObservationsAll(context.Background(), "project")
	noErr(t, err)
	restore := failGit(t, f, "git-fails-ancestry", "merge-base", failMergeBase)
	run, err := f.refresh()
	restore()
	after, err2 := f.store.ImportObservationsAll(context.Background(), "project")
	noErr(t, err2)
	require(t, reflect.DeepEqual(before, after),
		"failed ancestry advanced observations: before=%+v after=%+v", before, after)
	require(t, err != nil && run.Status != state.ImportRunComplete,
		"ancestry execution failure became a completed divergent run")
	// A healthy refresh afterwards fast-forwards: the fault left no false completion.
	retry, err := f.refresh()
	noErr(t, err)
	require(t, retry.RefsDivergent == 0 && f.destinationRefs()["refs/heads/dev"] == upstream,
		"healthy retry stayed divergent: %+v", retry)
}

func TestAncestryExitOneIsAProvenNonAncestor(t *testing.T) {
	f := newFixture(t)
	f.commit("base", "base\n")
	f.git(f.source, "branch", "other")
	main := f.commit("main", "main\n")
	f.git(f.source, "checkout", "other")
	other := f.commit("other", "other\n")
	f.mustImport(ImportInput{})
	ancestor, err := f.service.isAncestor(context.Background(), &runState{limits: DefaultLimits()}, f.destinationPath(), main, other)
	noErr(t, err)
	require(t, !ancestor, "unrelated branches were reported as ancestors")
}

func TestHistoricalObservationMustBeExplicitlyMissing(t *testing.T) {
	f := newFixture(t)
	f.commit("base", "base\n")
	f.mustImport(ImportInput{})
	run := &runState{limits: DefaultLimits()}
	missing := []string{strings.Repeat("f", 40)}
	present, err := f.service.objectsPresent(context.Background(), run, f.destinationPath(), missing)
	noErr(t, err)
	require(t, !present, "missing observation was reported as present")
	restore := failGit(t, f, "git-malformed-object-inspection", "--batch-check", "echo malformed; exit 0")
	_, err = f.service.objectsPresent(context.Background(), run, f.destinationPath(), missing)
	restore()
	require(t, problemCode(err) == CodeVerifyFailed, "malformed object inspection was treated as missing: %v", err)
}

func TestAncestryExecutionFailureIsNotAProtectedRewrite(t *testing.T) {
	f := newFixture(t)
	f.commit("first", "first\n")
	f.mustImport(ImportInput{})
	upstream := f.commit("second", "second\n")
	protect := true
	_, err := f.store.SaveRepositoryRefPolicy(context.Background(), "project", state.RepositoryRefPolicyChange{ProtectDefaultBranch: &protect})
	noErr(t, err)
	restore := failGit(t, f, "git-fails-ancestry", "merge-base", failMergeBase)
	f.refreshFails(CodePublishFailed, state.ImportRunFailed)
	restore()
	_, err = f.service.Status(context.Background(), "project")
	noErr(t, err)
	_, err = f.refresh()
	noErr(t, err)
	eq(t, "main after the healthy refresh", f.destinationRefs()["refs/heads/main"], upstream)
}
