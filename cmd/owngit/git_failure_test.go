package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/apiclient"
)

// A Git command that fails in the user's clone is reported with its name and
// Git's reason, as the server reports its own Git failures, not with an exit
// status alone.
func TestCLIGitFailuresNameTheCommandAndReason(t *testing.T) {
	ctx := context.Background()
	work := newClone(t) // no commit yet, so HEAD does not resolve

	_, _, err := inspectWorktree(ctx, work)
	if err == nil || !strings.Contains(err.Error(), ": git rev-parse: exit status 128: fatal: ") {
		t.Errorf("worktree inspection err=%v, want git rev-parse and Git's reason", err)
	}

	_, err = committedCheckDefinitions(ctx, work, strings.Repeat("0", 40))
	if err == nil || !strings.Contains(err.Error(), ": git ls-tree: exit status 128: fatal: ") {
		t.Errorf("committed configuration read err=%v, want git ls-tree and Git's reason", err)
	}

	noErr(t, os.WriteFile(filepath.Join(work, ".git", "config"), []byte("[core\n"), 0o600))
	_, err = readOriginRemote(ctx, work)
	var problem *apiclient.Error
	if !errors.As(err, &problem) || problem.Code != "origin_unavailable" {
		t.Fatalf("origin read err=%v, want origin_unavailable", err)
	}
	if problem.Cause == nil || !strings.HasPrefix(problem.Cause.Error(), "git config: exit status 128: fatal: bad config line 1") {
		t.Errorf("origin read cause=%v, want git config and Git's reason", problem.Cause)
	}
}
