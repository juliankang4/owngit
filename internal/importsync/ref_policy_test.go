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
// protection as they were when it started. A replaced tip is kept only
// while history is kept. While the default branch is protected, a refresh
// that would rewrite it is refused and changes nothing, however often the
// source moves on; once the protection is off, the next refresh follows the
// source again.
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
			saveRepository := func(change state.RepositoryRefPolicyChange) {
				_, err := f.store.SaveRepositoryRefPolicy(ctx, "project", change)
				noErr(t, err)
			}

			// History is kept by default; turning it off while a refresh runs
			// applies to the next refresh.
			off := false
			f.service.beforeStagingVerification = func(context.Context) {
				noErr(t, f.store.SavePolicies(ctx, state.PolicyChange{KeptHistory: &off}))
			}
			second := amend("main", "second main\n")
			run, err := f.refresh()
			noErr(t, err, "refresh")
			f.service.beforeStagingVerification = nil
			refs := f.destinationRefs()
			require(t, run.RefsUpdated == 1 && refs["refs/heads/main"] == second &&
				refs[repository.RetainedRefName("heads", first)] == first,
				"refresh that started with kept history: run=%+v refs=%v", run, refs)
			rewritten := amend("main", "rewritten main\n")
			run, err = f.refresh()
			noErr(t, err, "refresh")
			refs = f.destinationRefs()
			require(t, run.RefsUpdated == 1 && refs["refs/heads/main"] == rewritten &&
				refs[repository.RetainedRefName("heads", first)] == first,
				"run=%+v main=%s want %s", run, refs["refs/heads/main"], rewritten)
			for _, name := range []string{repository.RetainedRefName("heads", second), repository.ProvenanceRefName("heads", "main", second)} {
				require(t, refs[name] == "", "a replaced tip was kept while history is not: %s", name)
			}

			protect, unprotect := true, false
			saveRepository(state.RepositoryRefPolicyChange{ProtectDefaultBranch: &protect})
			devBefore := refs["refs/heads/dev"]
			amend("main", "rewritten again\n")
			amend("dev", "rewritten dev\n")
			refused := func(what string) {
				t.Helper()
				_, err := f.refresh()
				var problem *Problem
				require(t, errors.As(err, &problem) && problem.Code == CodeProtectedBranch,
					"%s: err=%v, want %s", what, err, CodeProtectedBranch)
				refs := f.destinationRefs()
				require(t, refs["refs/heads/main"] == rewritten && refs["refs/heads/dev"] == devBefore,
					"%s changed main=%s dev=%s", what, refs["refs/heads/main"], refs["refs/heads/dev"])
			}
			refused("a rewrite of the protected default branch")
			f.git(f.source, "checkout", "--quiet", "main")
			sourceMain := f.commit("more", "more on main\n")
			refused("a source that moved on from its rewrite")

			// A change saved while a refresh runs applies to the next one.
			f.service.beforeStagingVerification = func(context.Context) {
				saveRepository(state.RepositoryRefPolicyChange{ProtectDefaultBranch: &unprotect})
			}
			refused("a refresh that started while protection was on")
			f.service.beforeStagingVerification = nil
			run, err = f.refresh()
			noErr(t, err, "refresh after protection was turned off")
			refs = f.destinationRefs()
			require(t, run.RefsUpdated == 2 && refs["refs/heads/main"] == sourceMain &&
				refs["refs/heads/dev"] != devBefore,
				"after protection off run=%+v main=%s want %s", run, refs["refs/heads/main"], sourceMain)
			status, err := f.service.Status(ctx, "project")
			noErr(t, err)
			for _, ref := range status.Refs {
				require(t, ref.State == "tracked", "ref %s is %s after following the source", ref.Name, ref.State)
			}

			// A saved choice that cannot be read stops the refresh before it
			// writes anything.
			devTip := refs["refs/heads/dev"]
			noErr(t, f.store.Exec(ctx,
				`PRAGMA ignore_check_constraints=ON; UPDATE repository_policies SET retain_history=3 WHERE repository_id='project'; PRAGMA ignore_check_constraints=OFF`))
			f.git(f.source, "checkout", "--quiet", "dev")
			f.commit("more dev", "more dev\n")
			_, err = f.refresh()
			var problem *Problem
			require(t, errors.As(err, &problem) && problem.Code == CodeStateUnavailable,
				"refresh with an unreadable choice: err=%v", err)
			eq(t, "dev while the choice could not be read", f.destinationRefs()["refs/heads/dev"], devTip)
		})
	}
}
