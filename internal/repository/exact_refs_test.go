package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBranchSelectionIdentityAndControls(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "one", "base", "2024-01-01T00:00:00Z")
	base := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "HEAD:refs/heads/x", "HEAD:refs/heads/topic/slash", "HEAD:refs/tags/topic/slash")
	commitFile(t, work, "two", "long", "2024-01-02T00:00:00Z")
	long := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/refs/heads/x")
	for _, check := range []struct {
		input, full string
		exact       bool
	}{
		{"main", "refs/heads/main", false},
		{"refs/heads/main", "refs/heads/main", false},
		{"topic/slash", "refs/heads/topic/slash", false},
		{"refs/heads/topic/slash", "refs/heads/topic/slash", true},
		{"x", "refs/heads/x", false},
		{"refs/heads/x", "refs/heads/x", true},
		{"refs/heads/refs/heads/x", "refs/heads/refs/heads/x", false},
		{"refs/heads/refs/heads/x", "refs/heads/refs/heads/x", true},
	} {
		full, err := manager.SetDefaultBranchInput(t.Context(), "sample", check.input, check.exact)
		if err != nil || full != check.full {
			t.Fatalf("select %q exact=%v: full=%s err=%v", check.input, check.exact, full, err)
		}
		if head := gitOutput(t, remote, "symbolic-ref", "HEAD"); head != check.full {
			t.Fatalf("HEAD=%s, want %s", head, check.full)
		}
	}
	before := gitOutput(t, remote, "symbolic-ref", "HEAD")
	_, err := manager.SetDefaultBranchInput(t.Context(), "sample", "refs/heads/x", false)
	var ambiguous *AmbiguousBranchError
	if !errors.As(err, &ambiguous) || len(ambiguous.Refs) != 2 {
		t.Fatalf("ambiguity=%v", err)
	}
	for _, input := range []string{"missing", "refs/heads/missing", "../bad"} {
		if err := manager.SetDefaultBranch(t.Context(), "sample", input); !errors.Is(err, ErrBranchNotFound) {
			t.Fatalf("missing %q error=%v", input, err)
		}
	}
	if got := gitOutput(t, remote, "symbolic-ref", "HEAD"); got != before {
		t.Fatalf("refusal changed HEAD=%s", got)
	}
	for _, check := range []struct{ input, full, oid string }{
		{"refs/heads/x", "refs/heads/x", base},
		{"refs/heads/refs/heads/x", "refs/heads/refs/heads/x", long},
		{"topic/slash", "refs/heads/topic/slash", base},
		{"refs/tags/topic/slash", "refs/tags/topic/slash", base},
	} {
		full, oid, err := manager.ResolveRef(t.Context(), "sample", check.input)
		if err != nil || full != check.full || oid != check.oid {
			t.Fatalf("browse %q = %s %s %v", check.input, full, oid, err)
		}
	}
}

func TestLegacyBranchSelectionSingleInterpretationAndChain(t *testing.T) {
	manager, remote, work := newTestRepository(t)
	commitFile(t, work, "file", "base", "2024-01-01T00:00:00Z")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/refs/heads/x")
	full, err := manager.SetDefaultBranchInput(t.Context(), "sample", "refs/heads/x", false)
	noErr(t, err)
	if full != "refs/heads/refs/heads/x" {
		t.Fatalf("single existing interpretation=%s", full)
	}
	runGit(t, remote, "update-ref", "refs/heads/x", gitOutput(t, work, "rev-parse", "HEAD"))
	runGit(t, work, "push", "origin", "HEAD:refs/heads/refs/heads/refs/heads/x")
	for _, input := range []string{"refs/heads/x", "refs/heads/refs/heads/x"} {
		_, err := manager.SelectBranchRef(t.Context(), remote, input, false)
		var ambiguous *AmbiguousBranchError
		if !errors.As(err, &ambiguous) {
			t.Fatalf("chain input %q did not refuse: %v", input, err)
		}
	}
	full, err = manager.SetDefaultBranchInput(t.Context(), "sample", "refs/heads/refs/heads/x", true)
	noErr(t, err)
	if full != "refs/heads/refs/heads/x" {
		t.Fatal("exact chain middle was reinterpreted")
	}
}

func TestSnapshotSelectionCachedObjectsDoNotWaitForWriter(t *testing.T) {
	manager, _, work := newTestRepository(t)
	commitFile(t, work, "file", "base", "2024-01-01T00:00:00Z")
	runGit(t, work, "tag", "-a", "warm", "-m", "warm")
	runGit(t, work, "tag", "-a", "cold", "-m", "cold")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main", "refs/tags/warm", "refs/tags/cold")
	snapshot := mustSnapshot(t, manager)
	want := snapshot.Summary.DefaultOID
	oid, err := manager.RefCommitAt(t.Context(), "sample", snapshot, "refs/tags/warm")
	noErr(t, err)
	if oid != want {
		t.Fatalf("warm tag=%s, want %s", oid, want)
	}
	unowned := snapshot
	unowned.reads = nil
	if oid, err := manager.RefCommitAt(t.Context(), "sample", unowned, "refs/heads/main"); err == nil || oid != "" {
		t.Fatalf("unowned snapshot was accepted: %s %v", oid, err)
	}
	root := manager.Root
	manager.SetRoot(t.TempDir())
	oid, err = manager.RefCommitAt(t.Context(), "sample", snapshot, "refs/heads/main")
	manager.SetRoot(root)
	if err == nil || oid != "" {
		t.Fatalf("missing repository path was ignored: %s %v", oid, err)
	}
	lock := manager.Locks.For("sample")
	lock.Lock()
	defer lock.Unlock()
	for _, full := range []string{"refs/heads/main", "refs/tags/warm"} {
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		oid, err := manager.RefCommitAt(ctx, "sample", snapshot, full)
		cancel()
		if err != nil || oid != want {
			t.Fatalf("cached %s waited for a writer: %s %v", full, oid, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := manager.RefCommitAt(ctx, "sample", snapshot, "refs/tags/cold"); !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("uncached tag did not wait for the writer: %v", err)
	}
	snapshot.Stale = true
	if _, err := manager.RefCommitAt(t.Context(), "sample", snapshot, "refs/heads/main"); !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("stale commit was served as current: %v", err)
	}
}

func TestRefSelectionLookupFailureIsNotAbsence(t *testing.T) {
	failure := errors.New("lookup failed")
	calls := 0
	_, err := selectRefName("refs/heads/x", false, false, func(string) (bool, error) {
		calls++
		return false, failure
	}, nil)
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("lookup failure=%v calls=%d", err, calls)
	}
}
