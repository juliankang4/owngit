package recovery

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
)

// The opt-in fixtures are produced by an unchanged released Merge method,
// including its failed first attempt and successful changed-revision retry.
func TestReleasedAbandonedMergePlansRemainRecoverable(t *testing.T) {
	fixtures := os.Getenv("OWNGIT_RELEASED_MERGE_FIXTURES")
	if fixtures == "" {
		t.Skip("requires synthetic state produced by a released binary")
	}
	for _, status := range []string{state.MergeIntentPreparing, state.MergeIntentPlanned, state.MergeIntentReady} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			fixture := filepath.Join(fixtures, "current", status)
			stateDir := filepath.Join(fixture, "state")
			store, err := state.Open(ctx, stateDir)
			noErr(t, err)
			defer store.Close()
			settings, err := store.Settings(ctx)
			noErr(t, err)
			runner, err := gitexec.New("", filepath.Join(t.TempDir(), "runtime"))
			noErr(t, err)
			manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: settings.RepositoryRoot}
			before, err := store.RecoverySnapshot(ctx)
			noErr(t, err)
			foundAbandoned := false
			for _, intent := range before.PullRequestMergeIntents {
				foundAbandoned = foundAbandoned || intent.Status == status
			}
			if len(before.PullRequestMergeIntents) != 2 || !foundAbandoned || before.PullRequests[0].Status != state.PullRequestMerged {
				t.Fatalf("released fixture does not have an abandoned %s merge: %+v", status, before.PullRequestMergeIntents)
			}
			root := t.TempDir()
			backup := filepath.Join(root, "serving-backup")
			_, err = CreateWhileServing(ctx, store, manager, backup)
			noErr(t, err)
			manifest, err := readManifest(filepath.Join(backup, manifestName))
			noErr(t, err)
			if len(manifest.PullRequestMergeIntents) != 2 {
				t.Fatal("backup silently discarded the abandoned intent")
			}
			verification, err := Verify(ctx, backup, root, "")
			noErr(t, err)
			if !verification.Verified {
				t.Fatalf("verification: %+v", verification)
			}
			restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
			restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
			noErr(t, Restore(ctx, backup, restoredState, restoredRepositories, ""))
			remote := filepath.Join(restoredRepositories, "project.git")
			assertRef(t, remote, "refs/heads/main", before.PullRequests[0].MergeOID)
			runGit(t, "", "--git-dir", remote, "fsck", "--full")
			assertReceiptControls(t, manifest)

			// One normal startup preparation must make the original repository usable.
			service := &pullrequest.Service{Store: store, Repositories: manager}
			preparation, stop := context.WithCancel(ctx)
			t.Cleanup(func() { stop(); noErr(t, manager.StopPreparation(ctx)) })
			noErr(t, manager.StartPreparation(preparation, service.RecoverRepositoryLocked, 10*time.Second, nil))
			if manager.Preparing("project") {
				t.Fatal("released abandoned merge kept the repository locked after restart")
			}
			shown, err := service.Show(ctx, "project", before.PullRequests[0].Number)
			noErr(t, err)
			if shown.Merge == nil || shown.Merge.OID != before.PullRequests[0].MergeOID {
				t.Fatalf("recovered pull request: %+v", shown)
			}
			_, err = CreateWithReport(ctx, store, manager, filepath.Join(root, "offline-backup"))
			noErr(t, err)
			after, err := store.RecoverySnapshot(ctx)
			noErr(t, err)
			if !reflect.DeepEqual(before.PullRequestMergeIntents, after.PullRequestMergeIntents) {
				t.Fatal("recovery changed stored released intents")
			}
		})
	}
}

func assertReceiptControls(t *testing.T, manifest Manifest) {
	t.Helper()
	for _, control := range []string{"mismatch", "missing", "unrelated"} {
		t.Run(control, func(t *testing.T) {
			copy := manifest
			copy.Repositories = append([]RepositoryManifest(nil), manifest.Repositories...)
			copy.Repositories[0].Refs = append([]Ref(nil), manifest.Repositories[0].Refs...)
			refs := copy.Repositories[0].Refs
			if control == "unrelated" {
				copy.Repositories[0].Refs = append(refs, Ref{Name: pullrequest.MergeReceiptRef(999), OID: manifest.PullRequests[0].MergeOID})
			} else {
				found := false
				for i, ref := range refs {
					if ref.Name != pullrequest.MergeReceiptRef(manifest.PullRequests[0].Number) {
						continue
					}
					found = true
					if control == "missing" {
						copy.Repositories[0].Refs = append(refs[:i], refs[i+1:]...)
					} else {
						refs[i].OID = manifest.PullRequests[0].MergeTargetOID
					}
					break
				}
				if !found {
					t.Fatal("receipt control has no receipt")
				}
			}
			if err := validateManifest(copy); err == nil {
				t.Fatalf("%s receipt was accepted", control)
			}
		})
	}
}
