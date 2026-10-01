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

func TestAncestryExecutionFailureIsNotACompletedDivergence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the execution fault uses a POSIX Git wrapper")
	}
	f := newFixture(t)
	f.commit("first", "first\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	local := f.localWork("dev", "local\n")
	f.git(f.source, "fetch", f.destinationPath(), "refs/heads/dev")
	f.git(f.source, "checkout", "-B", "dev", "FETCH_HEAD")
	upstream := f.commit("upstream includes local", "upstream\n")
	if f.git(f.source, "merge-base", "--is-ancestor", local, upstream) != "" {
		t.Fatal("fixture is not a fast-forward")
	}
	before, err := f.store.ImportObservationsAll(context.Background(), "project")
	noErr(t, err)
	original := f.manager.Git.GitPath
	wrapper := filepath.Join(f.root, "git-fails-ancestry")
	marker := filepath.Join(f.root, "ancestry-attempted")
	content := fmt.Sprintf("#!/bin/sh\nfor arg do\n if [ \"$arg\" = merge-base ]; then\n  printf 'attempted\\n' >> '%s'\n  echo 'fatal: synthetic object read failure' >&2\n  exit 128\n fi\ndone\nexec '%s' \"$@\"\n", marker, original)
	noErr(t, os.WriteFile(wrapper, []byte(content), 0700))
	f.manager.Git.GitPath = wrapper
	run, err := f.refresh()
	f.manager.Git.GitPath = original
	after, queryErr := f.store.ImportObservationsAll(context.Background(), "project")
	noErr(t, queryErr)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("failed ancestry advanced observations: before=%+v after=%+v", before, after)
	}
	attempts, readErr := os.ReadFile(marker)
	noErr(t, readErr)
	t.Logf("attempts=%d status=%s divergent=%d err=%v local=%s source=%s", strings.Count(string(attempts), "attempted"), run.Status, run.RefsDivergent, err, f.destinationRefs()["refs/heads/dev"], upstream)
	if err == nil || run.Status == state.ImportRunComplete {
		t.Errorf("ancestry execution failure became a completed divergent run")
	}
	retry, retryErr := f.refresh()
	noErr(t, retryErr)
	t.Logf("healthy retry after fault: divergent=%d local=%s source=%s", retry.RefsDivergent, f.destinationRefs()["refs/heads/dev"], upstream)
	if f.destinationRefs()["refs/heads/dev"] != upstream {
		t.Errorf("the false completion made the subsequent healthy retry stay divergent")
	}
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
	if ancestor {
		t.Fatal("unrelated branches were reported as ancestors")
	}
}

func TestAncestryHealthyFastForwardControl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the execution fault uses a POSIX Git wrapper")
	}
	f := newFixture(t)
	f.commit("first", "first\n")
	f.git(f.source, "branch", "dev")
	f.mustImport(ImportInput{})
	f.localWork("dev", "local\n")
	f.git(f.source, "fetch", f.destinationPath(), "refs/heads/dev")
	f.git(f.source, "checkout", "-B", "dev", "FETCH_HEAD")
	upstream := f.commit("upstream includes local", "upstream\n")
	run, err := f.refresh()
	noErr(t, err)
	t.Logf("healthy first refresh: status=%s divergent=%d local=%s source=%s", run.Status, run.RefsDivergent, f.destinationRefs()["refs/heads/dev"], upstream)
	if f.destinationRefs()["refs/heads/dev"] != upstream || run.RefsDivergent != 0 {
		t.Fatal("healthy fast-forward control failed")
	}
}

func TestAncestryExecutionFailureIsNotAProtectedRewrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the execution fault uses a POSIX Git wrapper")
	}
	f := newFixture(t)
	f.commit("first", "first\n")
	f.mustImport(ImportInput{})
	upstream := f.commit("second", "second\n")
	protect := true
	_, policyErr := f.store.SaveRepositoryRefPolicy(context.Background(), "project", state.RepositoryRefPolicyChange{ProtectDefaultBranch: &protect})
	noErr(t, policyErr)
	original := f.manager.Git.GitPath
	wrapper := filepath.Join(f.root, "git-fails-ancestry")
	content := fmt.Sprintf("#!/bin/sh\nfor arg do\n if [ \"$arg\" = merge-base ]; then\n  echo 'fatal: synthetic object read failure' >&2\n  exit 128\n fi\ndone\nexec '%s' \"$@\"\n", original)
	noErr(t, os.WriteFile(wrapper, []byte(content), 0700))
	f.manager.Git.GitPath = wrapper
	run, err := f.refresh()
	f.manager.Git.GitPath = original
	t.Logf("status=%s class=%s err=%v", run.Status, run.ErrorClass, err)
	if problemCode(err) != CodePublishFailed || run.Status != state.ImportRunFailed {
		t.Errorf("ancestry execution failure was not reported honestly: %+v err=%v", run, err)
	}
	_, err = f.service.Status(context.Background(), "project")
	noErr(t, err)
	_, err = f.refresh()
	noErr(t, err)
	if f.destinationRefs()["refs/heads/main"] != upstream {
		t.Fatal("control did not follow main")
	}
}
