package importsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/repository"
	"owngit/internal/state"
)

// A refresh follows the repository's kept history and default branch
// protection as a push does: a replaced tip is kept only while history is
// kept, and the protected default branch follows only a fast-forward, so a
// source rewrite of it stays local as a diverged branch. Other branches
// still follow the source.
func TestRefreshFollowsKeptHistoryAndDefaultBranchProtection(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t)
			if format == "sha256" {
				f.format = format
				f.source = filepath.Join(f.root, "source-sha256")
				f.initSource()
			}
			first := f.commit("one", "one\n")
			f.git(f.source, "branch", "dev")
			f.mustImport(ImportInput{})
			// amend replaces the source's tip of branch with a new root.
			amend := func(branch, content string) string {
				f.git(f.source, "checkout", "--quiet", branch)
				noErr(t, os.WriteFile(filepath.Join(f.source, "file.txt"), []byte(content), 0o600))
				f.git(f.source, "add", "file.txt")
				f.git(f.source, "commit", "--quiet", "--amend", "-m", content)
				return f.git(f.source, "rev-parse", "HEAD")
			}

			off := false
			noErr(t, f.store.SavePolicies(ctx, state.PolicyChange{KeptHistory: &off}))
			rewritten := amend("main", "rewritten main\n")
			run, err := f.refresh()
			noErr(t, err, "refresh")
			refs := f.destinationRefs()
			if run.RefsUpdated != 1 || refs["refs/heads/main"] != rewritten {
				t.Fatalf("run=%+v main=%s want %s", run, refs["refs/heads/main"], rewritten)
			}
			for _, name := range []string{repository.RetainedRefName("heads", first), repository.ProvenanceRefName("heads", "main", first)} {
				if refs[name] != "" {
					t.Fatalf("a replaced tip was kept while history is not: %s", name)
				}
			}

			protect := true
			_, err = f.store.SaveRepositoryRefPolicy(ctx, "project", state.RepositoryRefPolicyChange{ProtectDefaultBranch: &protect})
			noErr(t, err)
			amend("main", "rewritten again\n")
			devTip := amend("dev", "rewritten dev\n")
			run, err = f.refresh()
			noErr(t, err, "refresh")
			refs = f.destinationRefs()
			if run.RefsDivergent != 1 || run.RefsUpdated != 1 || refs["refs/heads/main"] != rewritten || refs["refs/heads/dev"] != devTip {
				t.Fatalf("protected refresh run=%+v main=%s dev=%s", run, refs["refs/heads/main"], refs["refs/heads/dev"])
			}

			// A saved choice that cannot be read stops the refresh before it
			// writes anything.
			noErr(t, f.store.Exec(ctx, `PRAGMA ignore_check_constraints=ON; UPDATE repository_policies SET retain_history=3 WHERE repository_id='project'; PRAGMA ignore_check_constraints=OFF`))
			f.git(f.source, "checkout", "--quiet", "dev")
			f.commit("more", "more dev\n")
			_, err = f.refresh()
			var problem *Problem
			if !errors.As(err, &problem) || problem.Code != CodeStateUnavailable {
				t.Fatalf("refresh with an unreadable choice: err=%v", err)
			}
			if got := f.destinationRefs()["refs/heads/dev"]; got != devTip {
				t.Fatalf("dev moved to %s while the choice could not be read", got)
			}
		})
	}
}
