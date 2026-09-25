package recovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// A merge that found the source already in the target records no new commit,
// and a closed pull request stays in the history. Both need backup format 10:
// they survive backup and restore, and a version 9 manifest cannot carry them.
func TestBackupRestoresAnUpToDateMergeAndAClosedPullRequest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	work := filepath.Join(root, "backup-work")
	remote, err := manager.Path("project")
	noErr(t, err)
	service := &pullrequest.Service{Store: store, Repositories: manager}
	runGit(t, work, "remote", "add", "origin", remote)
	runGit(t, work, "checkout", "-b", "feature")
	noErr(t, os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o600))
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "feature")
	sourceOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/feature")
	created, err := service.Create(ctx, pullrequest.CreateInput{Repository: "project", Title: "Already in main", SourceBranch: "feature", TargetBranch: "main"})
	noErr(t, err)
	runGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	merged, err := service.Merge(ctx, "project", created.Number, pullrequest.RevisionInput{SourceOID: sourceOID, TargetOID: sourceOID})
	noErr(t, err)
	if merged.Merge == nil || merged.Merge.Mode != "up_to_date" || merged.Merge.OID != sourceOID {
		t.Fatalf("merge=%+v, want up_to_date at %s", merged.Merge, sourceOID)
	}

	runGit(t, work, "checkout", "-b", "abandoned")
	noErr(t, os.WriteFile(filepath.Join(work, "abandoned.txt"), []byte("abandoned\n"), 0o600))
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "abandoned")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/abandoned")
	abandoned, err := service.Create(ctx, pullrequest.CreateInput{Repository: "project", Title: "Not needed", SourceBranch: "abandoned", TargetBranch: "main"})
	noErr(t, err)
	_, err = service.Close(ctx, "project", abandoned.Number)
	noErr(t, err)

	backup := filepath.Join(root, "backup")
	noErr(t, Create(ctx, store, manager, backup))
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	noErr(t, err)
	if manifest.Version != 10 {
		t.Fatalf("backup version=%d, want 10", manifest.Version)
	}
	for _, test := range []struct {
		name string
		edit func(*Manifest)
		want string
	}{
		{"up-to-date merge", func(m *Manifest) {
			for index := range m.PullRequests {
				if m.PullRequests[index].Status == state.PullRequestClosed {
					m.PullRequests[index].Status = state.PullRequestOpen
				}
			}
		}, "version 9 backup contains an unsupported up-to-date merge"},
		{"closed pull request", func(m *Manifest) {
			m.PullRequestMergeIntents = nil
		}, "version 9 backup contains an unsupported closed pull request"},
	} {
		older := manifest
		older.PullRequests = append([]PullRequestManifest(nil), manifest.PullRequests...)
		older.Version = checkBackupVersion
		test.edit(&older)
		if err := validateManifest(older); err == nil || err.Error() != test.want {
			t.Fatalf("%s in a version 9 manifest: err=%v", test.name, err)
		}
	}
	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
	noErr(t, Restore(ctx, backup, restoredState, restoredRepositories, ""))
	restoredStore, err := state.Open(ctx, restoredState)
	noErr(t, err)
	defer restoredStore.Close()
	runner, err := gitexec.New("", filepath.Join(restoredState, "runtime-test"))
	noErr(t, err)
	restoredManager := &repository.Manager{Store: restoredStore, Git: runner, Locks: gitexec.NewLocks(), Root: restoredRepositories}
	restoredService := &pullrequest.Service{Store: restoredStore, Repositories: restoredManager}
	noErr(t, restoredService.ReconcileAll(ctx))
	shown, err := restoredService.Show(ctx, "project", created.Number)
	noErr(t, err)
	if shown.State != state.PullRequestMerged || shown.Merge == nil || shown.Merge.Mode != "up_to_date" || shown.Merge.OID != sourceOID {
		t.Fatalf("restored pull request=%+v", shown.Merge)
	}
	closed, err := restoredService.Show(ctx, "project", abandoned.Number)
	if err != nil || closed.State != state.PullRequestClosed {
		t.Fatalf("restored closed pull request=%+v err=%v", closed, err)
	}
}
