package pullrequest

import (
	"testing"

	"owngit/internal/repository"
	"owngit/internal/state"
)

// OwnGit's own writes to the default branch, a pull request merge and a
// restore, are not pushes: they only add commits, so they succeed while the
// branch is protected, and turning kept history off does not remove what
// was kept before.
func TestMergeAndRestoreOntoAProtectedDefaultBranch(t *testing.T) {
	for _, format := range []string{repository.ObjectFormatSHA1, repository.ObjectFormatSHA256} {
		t.Run(format, func(t *testing.T) {
			fixture := newServiceFixtureWithFormat(t, format)
			first := fixture.commitFile("shared.txt", "one\n", "one")
			fixture.push("HEAD:refs/heads/main")
			// A rewrite pushed while history is kept keeps the replaced tip.
			fixture.git("commit", "--amend", "-m", "one again")
			fixture.git("push", "--force", "origin", "HEAD:refs/heads/main")
			retained := repository.RetainedRefName("heads", first)
			if fixture.ref(retained) != first {
				t.Fatal("the replaced tip was not kept")
			}

			off, on := false, true
			noErr(t, fixture.store.SavePolicies(fixture.ctx, state.PolicyChange{KeptHistory: &off}))
			_, err := fixture.store.SaveRepositoryRefPolicy(fixture.ctx, fixture.repositoryID, state.RepositoryRefPolicyChange{ProtectDefaultBranch: &on})
			noErr(t, err)

			fixture.git("checkout", "-b", "feature")
			sourceOID := fixture.commitFile("feature.txt", "feature\n", "feature")
			fixture.push("HEAD:refs/heads/feature")
			fixture.git("checkout", "main")
			targetOID := fixture.commitFile("main.txt", "main\n", "main")
			fixture.push("HEAD:refs/heads/main")
			created, err := fixture.service.Create(fixture.ctx, CreateInput{
				Repository: fixture.repositoryID, Title: "Merge the feature", SourceBranch: "feature", TargetBranch: "main", ReviewChoice: "skip",
			})
			noErr(t, err)
			merged, err := fixture.service.Merge(fixture.ctx, fixture.repositoryID, created.Number, RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
			noErr(t, err)
			if merged.Merge == nil || fixture.ref("refs/heads/main") != merged.Merge.OID {
				t.Fatalf("merge onto the protected default branch: %+v", merged.Merge)
			}

			request := repository.RestoreRequest{Source: first, Target: "main", Mode: repository.RestoreAll}
			preview, err := fixture.manager.PreviewRestore(fixture.ctx, fixture.repositoryID, request)
			noErr(t, err)
			request.ExpectedHead = preview.ExpectedHead
			restored, err := fixture.manager.ApplyRestore(fixture.ctx, fixture.repositoryID, request)
			noErr(t, err)
			if fixture.ref("refs/heads/main") != restored.CommitOID {
				t.Fatalf("restore onto the protected default branch: %+v", restored)
			}
			fixture.git("--git-dir", fixture.remote, "merge-base", "--is-ancestor", merged.Merge.OID, restored.CommitOID)
			if fixture.ref(retained) != first {
				t.Fatal("a kept tip was removed after kept history was turned off")
			}
		})
	}
}
