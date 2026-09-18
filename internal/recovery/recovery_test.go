package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/gitexec"
	"owngit/internal/pullrequest"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestOfflineBackupRestorePreservesPortableStateAndAllRefs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(root, "source-state")
	repositoriesRoot := filepath.Join(root, "source-repositories")
	if err := os.Mkdir(repositoriesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	accessHash, _ := auth.HashPassword("shared-password")
	adminHash, _ := auth.HashPassword("admin-password")
	if err := store.CompleteSetup(ctx, repositoriesRoot, "password", accessHash, adminHash, true); err != nil {
		t.Fatal(err)
	}
	runner, err := gitexec.New("", filepath.Join(stateDir, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	if _, err := manager.Create(ctx, "project", "portable metadata"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(ctx, "empty", "unborn main"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(ctx, "detached", "detached HEAD"); err != nil {
		t.Fatal(err)
	}
	settings, _ := store.Settings(ctx)
	if err := store.CreateSession(ctx, "general-session", "general", "csrf", settings.AccessSessionVersion, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.AddTrustedHost(ctx, "private-host.example"); err != nil {
		t.Fatal(err)
	}

	remote, _ := manager.Path("project")
	work := filepath.Join(root, "work")
	runGit(t, "", "init", "--initial-branch=main", work)
	runGit(t, work, "config", "user.name", "Backup Test")
	runGit(t, work, "config", "user.email", "backup@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "data.txt"), []byte("portable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "portable commit")
	oid := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "tag", "v1")
	runGit(t, work, "remote", "add", "origin", remote)
	runGit(t, work, "push", "origin", "--all")
	runGit(t, work, "push", "origin", "--tags")
	hidden := "refs/owngit/retained/heads/" + oid
	runGit(t, "", "--git-dir", remote, "update-ref", hidden, oid)
	detachedRemote, _ := manager.Path("detached")
	runGit(t, work, "push", detachedRemote, "HEAD:refs/heads/temporary")
	runGit(t, "", "--git-dir", detachedRemote, "update-ref", "--no-deref", "HEAD", oid)
	runGit(t, "", "--git-dir", detachedRemote, "update-ref", "-d", "refs/heads/temporary")

	backup := filepath.Join(root, "backup")
	if err := Create(ctx, store, manager, backup); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
	if err := Restore(ctx, backup, restoredState, restoredRepositories, ""); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := state.Open(ctx, restoredState)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	restoredSettings, err := restoredStore.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if restoredSettings.RepositoryRoot != restoredRepositories || restoredSettings.AccessMode != "password" || restoredSettings.InsecureHTTPAccepted {
		t.Fatalf("unexpected restored settings: %+v", restoredSettings)
	}
	if got, _ := restoredStore.PasswordHash(ctx, "access"); got != accessHash {
		t.Fatal("access password hash was not restored")
	}
	if got, _ := restoredStore.PasswordHash(ctx, "admin"); got != adminHash {
		t.Fatal("administrator password hash was not restored")
	}
	if _, ok, err := restoredStore.Session(ctx, "general-session", "general", time.Now()); err != nil || ok {
		t.Fatalf("source session was restored: ok=%v err=%v", ok, err)
	}
	if hosts, err := restoredStore.TrustedHosts(ctx); err != nil || len(hosts) != 0 {
		t.Fatalf("trusted hosts were restored: hosts=%v err=%v", hosts, err)
	}
	repositories, err := restoredStore.Repositories(ctx)
	if err != nil || len(repositories) != 3 {
		t.Fatalf("restored repositories=%v err=%v", repositories, err)
	}

	restoredRemote := filepath.Join(restoredRepositories, "project.git")
	assertRef(t, restoredRemote, "refs/heads/main", oid)
	assertRef(t, restoredRemote, "refs/tags/v1", oid)
	assertRef(t, restoredRemote, hidden, oid)
	if head := gitOutput(t, "", "--git-dir", restoredRemote, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("restored HEAD=%q", head)
	}
	emptyRemote := filepath.Join(restoredRepositories, "empty.git")
	if head := gitOutput(t, "", "--git-dir", emptyRemote, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("empty repository HEAD=%q", head)
	}
	if output, err := gitCombined("", "--git-dir", emptyRemote, "for-each-ref"); err != nil || strings.TrimSpace(output) != "" {
		t.Fatalf("empty repository gained refs: %q err=%v", output, err)
	}
	restoredDetached := filepath.Join(restoredRepositories, "detached.git")
	if head := gitOutput(t, "", "--git-dir", restoredDetached, "rev-parse", "--verify", "HEAD"); head != oid {
		t.Fatalf("detached HEAD=%q, want %q", head, oid)
	}
	if output, err := gitCombined("", "--git-dir", restoredDetached, "for-each-ref"); err != nil || strings.TrimSpace(output) != "" {
		t.Fatalf("detached repository gained refs: %q err=%v", output, err)
	}

	runGit(t, work, "checkout", "--orphan", "replacement")
	runGit(t, work, "rm", "-rf", ".")
	if err := os.WriteFile(filepath.Join(work, "replacement.txt"), []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "replacement")
	runGit(t, work, "remote", "add", "restored", restoredRemote)
	runGit(t, work, "push", "--force", "restored", "HEAD:refs/heads/main")
	assertRef(t, restoredRemote, "refs/owngit/retained/heads/"+oid, oid)
	assertRef(t, restoredRemote, "refs/owngit/provenance/heads/main/"+oid, oid)
}

func TestBackupV2RoundTripPreservesPullRequestsReviewsRevisionsAndReceipts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	work := filepath.Join(root, "backup-work")
	remote, err := manager.Path("project")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	service := &pullrequest.Service{Store: store, Repositories: manager}
	baseOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "remote", "add", "origin", remote)

	runGit(t, work, "checkout", "-b", "reviewed")
	if err := os.WriteFile(filepath.Join(work, "reviewed.txt"), []byte("reviewed\n"), 0o600); err != nil {
		store.Close()
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "reviewed change")
	reviewedOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/reviewed")
	openPR, err := service.Create(ctx, pullrequest.CreateInput{
		Repository: "project", Title: "Needs another review", SourceBranch: "reviewed", TargetBranch: "main", ReviewChoice: "request",
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := service.SubmitReview(ctx, "project", openPR.Number, pullrequest.ReviewSubmitInput{
		SourceOID: reviewedOID, TargetOID: baseOID, Decision: state.ReviewChangesRequested, ReviewerLabel: "existing-tool: recovery-test",
	}); err != nil {
		store.Close()
		t.Fatal(err)
	}

	runGit(t, work, "checkout", "main")
	runGit(t, work, "checkout", "-b", "merge-ready")
	if err := os.WriteFile(filepath.Join(work, "merged.txt"), []byte("merged\n"), 0o600); err != nil {
		store.Close()
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "merge-ready change")
	mergeSourceOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/merge-ready")
	mergedPR, err := service.Create(ctx, pullrequest.CreateInput{
		Repository: "project", Title: "Ready to merge", SourceBranch: "merge-ready", TargetBranch: "main", ReviewChoice: "skip",
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	mergedView, err := service.Merge(ctx, "project", mergedPR.Number, pullrequest.RevisionInput{SourceOID: mergeSourceOID, TargetOID: baseOID})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if mergedView.Merge == nil {
		store.Close()
		t.Fatal("merged pull request has no receipt")
	}
	if _, err := service.Show(ctx, "project", openPR.Number); err != nil {
		store.Close()
		t.Fatal(err)
	}

	backup := filepath.Join(root, "pr-backup")
	if err := Create(ctx, store, manager, backup); err != nil {
		store.Close()
		t.Fatal(err)
	}
	manifest, err := readManifest(filepath.Join(backup, manifestName))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if manifest.Version != backupVersion || len(manifest.PullRequests) != 2 || len(manifest.PullRequestReviews) != 3 || len(manifest.PullRequestMergeIntents) != 1 {
		store.Close()
		t.Fatalf("backup manifest pull request state: version=%d prs=%d reviews=%d intents=%d", manifest.Version, len(manifest.PullRequests), len(manifest.PullRequestReviews), len(manifest.PullRequestMergeIntents))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-pr-state"))
	restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "restored-pr-repositories"))
	if err := Restore(ctx, backup, restoredState, restoredRepositories, ""); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := state.Open(ctx, restoredState)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	snapshot, err := restoredStore.RecoverySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PullRequests) != 2 || len(snapshot.PullRequestReviews) != 3 || len(snapshot.PullRequestMergeIntents) != 1 {
		t.Fatalf("restored pull request snapshot: prs=%d reviews=%d intents=%d", len(snapshot.PullRequests), len(snapshot.PullRequestReviews), len(snapshot.PullRequestMergeIntents))
	}
	statuses := map[int64]string{}
	for _, record := range snapshot.PullRequests {
		statuses[record.Number] = record.Status
	}
	if statuses[openPR.Number] != state.PullRequestOpen || statuses[mergedPR.Number] != state.PullRequestMerged {
		t.Fatalf("restored statuses=%v", statuses)
	}
	restoredRemote := filepath.Join(restoredRepositories, "project.git")
	if got := gitOutput(t, "", "--git-dir", restoredRemote, "rev-parse", "--verify", pullrequest.MergeReceiptRef(mergedPR.Number)); got != mergedView.Merge.OID {
		t.Fatalf("restored merge receipt=%s, want %s", got, mergedView.Merge.OID)
	}
	openSourceRef, openTargetRef := pullrequest.RevisionRefNames(openPR.Number, reviewedOID, baseOID)
	assertRef(t, restoredRemote, openSourceRef, reviewedOID)
	assertRef(t, restoredRemote, openTargetRef, baseOID)
	restoredRunner, err := gitexec.New("", filepath.Join(restoredState, "runtime-test"))
	if err != nil {
		t.Fatal(err)
	}
	restoredManager := &repository.Manager{Store: restoredStore, Git: restoredRunner, Locks: gitexec.NewLocks(), Root: restoredRepositories}
	restoredService := &pullrequest.Service{Store: restoredStore, Repositories: restoredManager}
	openView, err := restoredService.Show(ctx, "project", openPR.Number)
	if err != nil {
		t.Fatal(err)
	}
	if openView.State != state.PullRequestOpen || openView.Review.Status != "decision_required" {
		t.Fatalf("restored open pull request=%+v", openView)
	}
	if merged, err := restoredService.Show(ctx, "project", mergedPR.Number); err != nil || merged.Merge == nil || merged.Merge.OID != mergedView.Merge.OID {
		t.Fatalf("restored merged pull request=%+v err=%v", merged, err)
	}
}

func TestBackupRestorePreservesUnpublishedNonFastForwardMergeIntent(t *testing.T) {
	for _, intentStatus := range []string{state.MergeIntentPlanned, state.MergeIntentReady} {
		t.Run(intentStatus, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			store, manager := newBackupStore(t, root)
			work := filepath.Join(root, "backup-work")
			remote, err := manager.Path("project")
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			runGit(t, work, "remote", "add", "origin", remote)
			runGit(t, work, "checkout", "-b", "candidate")
			if err := os.WriteFile(filepath.Join(work, "candidate.txt"), []byte("candidate\n"), 0o600); err != nil {
				store.Close()
				t.Fatal(err)
			}
			runGit(t, work, "add", ".")
			runGit(t, work, "commit", "-m", "candidate change")
			sourceOID := gitOutput(t, work, "rev-parse", "HEAD")
			runGit(t, work, "push", "origin", "HEAD:refs/heads/candidate")
			runGit(t, work, "checkout", "main")
			if err := os.WriteFile(filepath.Join(work, "target.txt"), []byte("target\n"), 0o600); err != nil {
				store.Close()
				t.Fatal(err)
			}
			runGit(t, work, "add", ".")
			runGit(t, work, "commit", "-m", "target change")
			targetOID := gitOutput(t, work, "rev-parse", "HEAD")
			runGit(t, work, "push", "origin", "HEAD:refs/heads/main")

			service := &pullrequest.Service{Store: store, Repositories: manager}
			created, err := service.Create(ctx, pullrequest.CreateInput{
				Repository: "project", Title: "Unpublished merge candidate", SourceBranch: "candidate", TargetBranch: "main", ReviewChoice: "skip",
			})
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			intent, err := store.BeginPullRequestMerge(ctx, state.PullRequestMergeIntent{
				RepositoryID: "project", PullRequestNumber: created.Number, SourceOID: sourceOID, TargetOID: targetOID,
				ReceiptRef: pullrequest.MergeReceiptRef(created.Number), CreatedAt: time.Unix(1_900_000_000, 0).UTC(),
			})
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			treeResult, err := manager.Git.Run(ctx, "", nil, "--git-dir", remote, "merge-tree", "--write-tree", targetOID, sourceOID)
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			intent.Mode = "merge_commit"
			intent.TreeOID = strings.Fields(string(treeResult.Stdout))[0]
			treeRef := pullrequest.MergeTreeRef(created.Number, sourceOID, targetOID)
			if _, err := manager.Git.Run(ctx, "", nil, "--git-dir", remote, "update-ref", treeRef, intent.TreeOID); err != nil {
				store.Close()
				t.Fatal(err)
			}
			intent.Status = state.MergeIntentPlanned
			intent.UpdatedAt = intent.CreatedAt
			intent, err = store.UpdatePullRequestMergeIntent(ctx, intent)
			if err != nil {
				store.Close()
				t.Fatal(err)
			}
			expectedResult := createRecoveryMergeCommit(t, ctx, manager.Git, remote, created, intent)
			if intentStatus == state.MergeIntentReady {
				resultRef := pullrequest.MergeResultRef(created.Number, sourceOID, targetOID)
				if _, err := manager.Git.Run(ctx, "", nil, "--git-dir", remote, "update-ref", resultRef, expectedResult); err != nil {
					store.Close()
					t.Fatal(err)
				}
				intent.ResultOID = expectedResult
				intent.Status = state.MergeIntentReady
				intent.UpdatedAt = intent.CreatedAt.Add(time.Second)
				if _, err := store.UpdatePullRequestMergeIntent(ctx, intent); err != nil {
					store.Close()
					t.Fatal(err)
				}
			}
			if refExists(t, remote, pullrequest.MergeReceiptRef(created.Number)) {
				store.Close()
				t.Fatal("unpublished fixture unexpectedly has a merge receipt")
			}

			backup := filepath.Join(root, "unpublished-"+intentStatus+"-backup")
			realRunner := manager.Git
			var oldGitPath, mergeTreeMarker string
			if runtime.GOOS != "windows" {
				oldGitPath, mergeTreeMarker = newOldGitExecutable(t)
				oldBackupRunner, err := gitexec.New(oldGitPath, filepath.Join(root, "runtime-old-git-backup-"+intentStatus))
				if err != nil {
					store.Close()
					t.Fatal(err)
				}
				manager.Git = oldBackupRunner
			}
			if err := Create(ctx, store, manager, backup); err != nil {
				store.Close()
				t.Fatal(err)
			}
			manager.Git = realRunner
			if mergeTreeMarker != "" {
				if _, err := os.Stat(mergeTreeMarker); !os.IsNotExist(err) {
					store.Close()
					t.Fatalf("old Git backup invoked merge-tree: %v", err)
				}
			}
			resultRef := pullrequest.MergeResultRef(created.Number, sourceOID, targetOID)
			assertRef(t, remote, treeRef, intent.TreeOID)
			if intentStatus == state.MergeIntentReady {
				assertRef(t, remote, resultRef, expectedResult)
			} else if refExists(t, remote, resultRef) {
				store.Close()
				t.Fatal("planned backup reconciliation fabricated a ready merge result ref")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-"+intentStatus+"-state"))
			restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "restored-"+intentStatus+"-repositories"))
			if err := Restore(ctx, backup, restoredState, restoredRepositories, ""); err != nil {
				t.Fatal(err)
			}
			restoredStore, err := state.Open(ctx, restoredState)
			if err != nil {
				t.Fatal(err)
			}
			defer restoredStore.Close()
			restoredRunner, err := gitexec.New("", filepath.Join(restoredState, "runtime-merge-retry"))
			if err != nil {
				t.Fatal(err)
			}
			restoredManager := &repository.Manager{Store: restoredStore, Git: restoredRunner, Locks: gitexec.NewLocks(), Root: restoredRepositories}
			restoredService := &pullrequest.Service{Store: restoredStore, Repositories: restoredManager}
			restoredRemote := filepath.Join(restoredRepositories, "project.git")
			assertRef(t, restoredRemote, "refs/heads/candidate", sourceOID)
			assertRef(t, restoredRemote, "refs/heads/main", targetOID)
			assertRef(t, restoredRemote, treeRef, intent.TreeOID)
			if intentStatus == state.MergeIntentReady {
				assertRef(t, restoredRemote, resultRef, expectedResult)
			} else {
				if refExists(t, restoredRemote, resultRef) {
					t.Fatal("planned restore fabricated a ready merge result ref")
				}
				if objectExists(t, restoredRemote, expectedResult) {
					t.Fatal("planned restore unexpectedly retained the unreferenced merge result object")
				}
			}
			if refExists(t, restoredRemote, pullrequest.MergeReceiptRef(created.Number)) {
				t.Fatal("restore fabricated a publication receipt")
			}
			merged, err := restoredService.Merge(ctx, "project", created.Number, pullrequest.RevisionInput{SourceOID: sourceOID, TargetOID: targetOID})
			if err != nil {
				t.Fatalf("retry restored %s merge: %v", intentStatus, err)
			}
			if merged.Merge == nil || merged.Merge.OID != expectedResult || merged.Merge.Mode != "merge_commit" {
				t.Fatalf("restored %s merge result=%+v, want %s", intentStatus, merged.Merge, expectedResult)
			}
			assertRef(t, restoredRemote, "refs/heads/candidate", sourceOID)
			assertRef(t, restoredRemote, "refs/heads/main", expectedResult)
			assertRef(t, restoredRemote, resultRef, expectedResult)
			assertRef(t, restoredRemote, pullrequest.MergeReceiptRef(created.Number), expectedResult)
			sourceRef, targetRef := pullrequest.RevisionRefNames(created.Number, sourceOID, targetOID)
			assertRef(t, restoredRemote, sourceRef, sourceOID)
			assertRef(t, restoredRemote, targetRef, targetOID)

			if runtime.GOOS != "windows" {
				oldState := canonicalTestTarget(t, filepath.Join(root, "old-git-"+intentStatus+"-state"))
				oldRepositories := canonicalTestTarget(t, filepath.Join(root, "old-git-"+intentStatus+"-repositories"))
				if err := Restore(ctx, backup, oldState, oldRepositories, oldGitPath); err != nil {
					t.Fatalf("restore pending %s merge on old Git: %v", intentStatus, err)
				}
				if _, err := os.Stat(mergeTreeMarker); !os.IsNotExist(err) {
					t.Fatalf("old Git restore invoked merge-tree: %v", err)
				}
				oldStore, err := state.Open(ctx, oldState)
				if err != nil {
					t.Fatal(err)
				}
				defer oldStore.Close()
				oldRunner, err := gitexec.New(oldGitPath, filepath.Join(oldState, "runtime-old-git-merge"))
				if err != nil {
					t.Fatal(err)
				}
				oldManager := &repository.Manager{Store: oldStore, Git: oldRunner, Locks: gitexec.NewLocks(), Root: oldRepositories}
				oldService := &pullrequest.Service{Store: oldStore, Repositories: oldManager}
				oldRemote := filepath.Join(oldRepositories, "project.git")
				shown, err := oldService.Show(ctx, "project", created.Number)
				if err != nil || shown.State != state.PullRequestOpen {
					t.Fatalf("show restored pending pull request on old Git: view=%+v err=%v", shown, err)
				}
				if _, err := oldService.Merge(ctx, "project", created.Number, pullrequest.RevisionInput{SourceOID: sourceOID, TargetOID: targetOID}); recoveryProblemCode(err) != "unsupported_git" {
					t.Fatalf("old Git merge error=%v code=%q, want unsupported_git", err, recoveryProblemCode(err))
				}
				assertRef(t, oldRemote, "refs/heads/main", targetOID)
				if refExists(t, oldRemote, pullrequest.MergeReceiptRef(created.Number)) {
					t.Fatal("old Git merge fabricated a receipt")
				}
				if _, err := os.Stat(mergeTreeMarker); !os.IsNotExist(err) {
					t.Fatalf("old Git merge invoked merge-tree: %v", err)
				}
			}
		})
	}
}

func TestRestoreReconcilesGitPublishedMergeWithPendingSQLiteState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	work := filepath.Join(root, "backup-work")
	remote, err := manager.Path("project")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	runGit(t, work, "remote", "add", "origin", remote)
	baseOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "checkout", "-b", "pending-state")
	if err := os.WriteFile(filepath.Join(work, "pending.txt"), []byte("pending\n"), 0o600); err != nil {
		store.Close()
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "pending state merge")
	sourceOID := gitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "origin", "HEAD:refs/heads/pending-state")
	service := &pullrequest.Service{Store: store, Repositories: manager}
	created, err := service.Create(ctx, pullrequest.CreateInput{
		Repository: "project", Title: "Reconcile after restore", SourceBranch: "pending-state", TargetBranch: "main", ReviewChoice: "skip",
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	service.CompleteMerge = func(context.Context, state.PullRequestMergeIntent, time.Time) error {
		return errors.New("injected SQLite completion failure")
	}
	if _, err := service.Merge(ctx, "project", created.Number, pullrequest.RevisionInput{SourceOID: sourceOID, TargetOID: baseOID}); err == nil {
		store.Close()
		t.Fatal("injected state completion failure was not reported")
	}
	record, exists, err := store.PullRequest(ctx, "project", created.Number)
	if err != nil || !exists || record.Status != state.PullRequestOpen {
		store.Close()
		t.Fatalf("pre-backup pull request state=%+v exists=%v err=%v", record, exists, err)
	}
	assertRef(t, remote, "refs/heads/main", sourceOID)
	assertRef(t, remote, pullrequest.MergeReceiptRef(created.Number), sourceOID)

	backup := filepath.Join(root, "pending-merge-backup")
	if err := Create(ctx, store, manager, backup); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restoredState := canonicalTestTarget(t, filepath.Join(root, "pending-restored-state"))
	restoredRepositories := canonicalTestTarget(t, filepath.Join(root, "pending-restored-repositories"))
	if err := Restore(ctx, backup, restoredState, restoredRepositories, ""); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := state.Open(ctx, restoredState)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	restored, exists, err := restoredStore.PullRequest(ctx, "project", created.Number)
	if err != nil || !exists || restored.Status != state.PullRequestMerged || restored.MergeOID != sourceOID {
		t.Fatalf("restored reconciled pull request=%+v exists=%v err=%v", restored, exists, err)
	}
	assertRef(t, filepath.Join(restoredRepositories, "project.git"), pullrequest.MergeReceiptRef(created.Number), sourceOID)
}

func TestRestoreAcceptsStrictLegacyV1AndRejectsV1PullRequestFields(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	backup := filepath.Join(root, "legacy-backup")
	if err := Create(ctx, store, manager, backup); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(backup, manifestName)
	manifest, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Version = legacyBackupVersion
	manifest.PullRequests = nil
	manifest.PullRequestRevisions = nil
	manifest.PullRequestReviews = nil
	manifest.PullRequestMergeIntents = nil
	file, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, backup, canonicalTestTarget(t, filepath.Join(root, "legacy-state")), canonicalTestTarget(t, filepath.Join(root, "legacy-repositories")), ""); err != nil {
		t.Fatalf("strict version 1 backup was rejected: %v", err)
	}

	manifest.PullRequests = []PullRequestManifest{{RepositoryID: "project", Number: 1}}
	if err := validateManifest(manifest); err == nil || !strings.Contains(err.Error(), "version 1") {
		t.Fatalf("version 1 manifest accepted pull request metadata: %v", err)
	}
}

func TestManifestRejectsInvalidOrUnexpectedPasswordHashes(t *testing.T) {
	validHash, err := auth.HashPassword("valid-password")
	if err != nil {
		t.Fatal(err)
	}
	valid := Manifest{
		Format: backupFormat, Version: backupVersion, CreatedAt: time.Now().UTC(),
		AccessMode: "password", AccessHash: validHash, AdminHash: validHash,
	}
	if err := validateManifest(valid); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	invalidAdmin := valid
	invalidAdmin.AdminHash = "not-a-password-hash"
	if err := validateManifest(invalidAdmin); err == nil {
		t.Fatal("malformed administrator password hash was accepted")
	}
	invalidAccess := valid
	invalidAccess.AccessHash = "not-a-password-hash"
	if err := validateManifest(invalidAccess); err == nil {
		t.Fatal("malformed access password hash was accepted")
	}
	unexpectedAccess := valid
	unexpectedAccess.AccessMode = "open"
	if err := validateManifest(unexpectedAccess); err == nil {
		t.Fatal("open-access manifest with a password hash was accepted")
	}
}

func TestRestoreRejectsCorruptionAndExistingDestination(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	malformed := filepath.Join(root, "malformed-backup")
	if err := os.Mkdir(malformed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformed, manifestName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, malformed, filepath.Join(root, "malformed-state"), filepath.Join(root, "malformed-repositories"), ""); err == nil || !strings.Contains(err.Error(), "decode backup manifest") {
		t.Fatalf("malformed manifest error=%v", err)
	}

	store, manager := newBackupStore(t, root)
	defer store.Close()
	backup := filepath.Join(root, "backup")
	if err := Create(ctx, store, manager, backup); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(backup, "repositories", "project.bundle")
	file, err := os.OpenFile(bundle, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("corruption"); err != nil {
		t.Fatal(err)
	}
	file.Close()
	stateTarget := filepath.Join(root, "corrupt-state")
	repositoryTarget := filepath.Join(root, "corrupt-repositories")
	if err := Restore(ctx, backup, stateTarget, repositoryTarget, ""); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("corrupt restore error=%v", err)
	}
	if _, err := os.Stat(stateTarget); !os.IsNotExist(err) {
		t.Fatalf("corrupt restore created state destination: %v", err)
	}
	if _, err := os.Stat(repositoryTarget); !os.IsNotExist(err) {
		t.Fatalf("corrupt restore created repository destination: %v", err)
	}

	existing := filepath.Join(root, "existing-state")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "sentinel"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, backup, existing, filepath.Join(root, "unused-repositories"), ""); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing destination error=%v", err)
	}
	if content, err := os.ReadFile(filepath.Join(existing, "sentinel")); err != nil || string(content) != "preserve" {
		t.Fatalf("existing destination changed: content=%q err=%v", content, err)
	}
}

type bundleFailingRunner struct {
	delegate commandRunner
}

func (runner bundleFailingRunner) Run(ctx context.Context, directory string, stdin io.Reader, arguments ...string) (gitexec.Result, error) {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == "bundle" && arguments[index+1] == "create" {
			return gitexec.Result{}, errors.New("injected bundle failure")
		}
	}
	return runner.delegate.Run(ctx, directory, stdin, arguments...)
}

func TestCompletedDirectoryPublicationNeverReplacesExistingDestination(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "sentinel"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(stage, destination); err == nil {
		t.Fatal("no-replace publication replaced an existing destination")
	}
	if content, err := os.ReadFile(filepath.Join(destination, "sentinel")); err != nil || string(content) != "preserve" {
		t.Fatalf("existing destination changed: content=%q err=%v", content, err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("staged directory was lost after refused publication: %v", err)
	}
}

func TestBackupPublishesOnlyAfterBundleSuccessAndCleansCanceledStages(t *testing.T) {
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	defer store.Close()

	failedOutput := filepath.Join(root, "failed-backup")
	err := create(context.Background(), store, manager, bundleFailingRunner{delegate: manager.Git}, failedOutput)
	if err == nil || !strings.Contains(err.Error(), "injected bundle failure") {
		t.Fatalf("bundle failure error=%v", err)
	}
	assertNoRecoveryOutputOrStages(t, failedOutput, ".owngit-backup-")

	canceledOutput := filepath.Join(root, "canceled-backup")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Create(ctx, store, manager, canceledOutput); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled backup error=%v, want context cancellation", err)
	}
	assertNoRecoveryOutputOrStages(t, canceledOutput, ".owngit-backup-")
}

func TestBackupRejectsDestinationThroughAncestorSymlinkIntoState(t *testing.T) {
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	defer store.Close()
	alias := ancestorSymlink(t, root)
	output := filepath.Join(alias, "source-state", "inside-backup")
	if err := Create(context.Background(), store, manager, output); err == nil || !strings.Contains(err.Error(), "must not overlap") {
		t.Fatalf("ancestor-symlink backup overlap error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "source-state", "inside-backup")); !os.IsNotExist(err) {
		t.Fatalf("overlapping backup destination was created: %v", err)
	}
}

func TestRestoreRejectsTargetThroughAncestorSymlinkIntoBackup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	defer store.Close()
	backup := filepath.Join(root, "backup")
	if err := Create(ctx, store, manager, backup); err != nil {
		t.Fatal(err)
	}
	alias := ancestorSymlink(t, root)
	stateTarget := filepath.Join(alias, "backup", "inside-state")
	repositoryTarget := filepath.Join(root, "restored-repositories")
	if err := Restore(ctx, backup, stateTarget, repositoryTarget, ""); err == nil || !strings.Contains(err.Error(), "must not overlap") {
		t.Fatalf("ancestor-symlink restore overlap error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(backup, "inside-state")); !os.IsNotExist(err) {
		t.Fatalf("overlapping restore state was created: %v", err)
	}
	assertNoRecoveryOutputOrStages(t, repositoryTarget, ".owngit-restore-")
}

func TestPathOverlapTreatsCaseAliasesConservatively(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("case-insensitive alias rule applies on macOS and Windows")
	}
	parent := t.TempDir()
	if !pathsOverlap(filepath.Join(parent, "Restore-State"), filepath.Join(parent, "restore-state")) {
		t.Fatal("case-only target aliases were treated as separate paths")
	}
}

func ancestorSymlink(t *testing.T, target string) string {
	t.Helper()
	alias := filepath.Join(t.TempDir(), "ancestor-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("ancestor symlink is unavailable on this host: %v", err)
	}
	return alias
}

func TestGuardedStateBlocksOpenBetweenRestorePublications(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	defer store.Close()
	backup := filepath.Join(root, "backup")
	if err := Create(ctx, store, manager, backup); err != nil {
		t.Fatal(err)
	}
	stateTarget := canonicalTestTarget(t, filepath.Join(root, "guarded-state"))
	repositoryTarget := canonicalTestTarget(t, filepath.Join(root, "guarded-repositories"))
	published := make(chan struct{})
	resume := make(chan struct{})
	operations := defaultRestoreOperations()
	operations.rename = func(oldPath, newPath string) error {
		err := renameNoReplace(oldPath, newPath)
		if err == nil && newPath == stateTarget {
			close(published)
			<-resume
		}
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- restore(ctx, backup, stateTarget, repositoryTarget, "", operations)
	}()
	select {
	case <-published:
	case <-time.After(30 * time.Second):
		t.Fatal("restore did not reach its first publication")
	}
	opened, openErr := state.Open(ctx, stateTarget)
	if opened != nil {
		opened.Close()
	}
	if openErr == nil || !strings.Contains(openErr.Error(), "incomplete offline restore") {
		close(resume)
		t.Fatalf("state target was usable between restore publications: %v", openErr)
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	restored, err := state.Open(ctx, stateTarget)
	if err != nil {
		t.Fatalf("completed restored state did not open: %v", err)
	}
	restored.Close()
}

func TestRestorePreparationAndPublicationFailuresLeaveNoFinalDestinations(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	defer store.Close()
	backup := filepath.Join(root, "backup")
	if err := Create(ctx, store, manager, backup); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*restoreOperations, string, string)
	}{
		{
			name: "state reopen",
			mutate: func(operations *restoreOperations, _, _ string) {
				calls := 0
				operations.openState = func(ctx context.Context, directory string) (*state.Store, error) {
					calls++
					if calls == 2 {
						return nil, errors.New("injected state reopen failure")
					}
					return state.Open(ctx, directory)
				}
			},
		},
		{
			name: "hook generation",
			mutate: func(operations *restoreOperations, _, _ string) {
				operations.prepareExisting = func(context.Context, *repository.Manager, *gitexec.Runner) error {
					return errors.New("injected hook failure")
				}
			},
		},
		{
			name: "repository publication",
			mutate: func(operations *restoreOperations, _, repositoryTarget string) {
				operations.rename = func(oldPath, newPath string) error {
					if newPath == repositoryTarget {
						return errors.New("injected repository publication failure")
					}
					return os.Rename(oldPath, newPath)
				}
			},
		},
		{
			name: "state publication",
			mutate: func(operations *restoreOperations, stateTarget, _ string) {
				operations.rename = func(oldPath, newPath string) error {
					if newPath == stateTarget {
						return errors.New("injected state publication failure")
					}
					return os.Rename(oldPath, newPath)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stateTarget := canonicalTestTarget(t, filepath.Join(root, strings.ReplaceAll(test.name, " ", "-")+"-state"))
			repositoryTarget := canonicalTestTarget(t, filepath.Join(root, strings.ReplaceAll(test.name, " ", "-")+"-repositories"))
			operations := defaultRestoreOperations()
			test.mutate(&operations, stateTarget, repositoryTarget)
			if err := restore(ctx, backup, stateTarget, repositoryTarget, "", operations); err == nil {
				t.Fatal("injected restore failure unexpectedly succeeded")
			}
			assertNoRecoveryOutputOrStages(t, stateTarget, ".owngit-restore-")
			assertNoRecoveryOutputOrStages(t, repositoryTarget, ".owngit-restore-")
		})
	}
}

func TestRestorePreservesAndMarksPathsWhenPublicationRollbackFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	defer store.Close()
	backup := filepath.Join(root, "backup")
	if err := Create(ctx, store, manager, backup); err != nil {
		t.Fatal(err)
	}
	stateTarget := canonicalTestTarget(t, filepath.Join(root, "partial-state"))
	repositoryTarget := canonicalTestTarget(t, filepath.Join(root, "partial-repositories"))
	operations := defaultRestoreOperations()
	operations.rename = func(oldPath, newPath string) error {
		switch {
		case newPath == repositoryTarget:
			return errors.New("injected repository publication failure")
		case oldPath == stateTarget:
			return errors.New("injected rollback failure")
		default:
			return os.Rename(oldPath, newPath)
		}
	}
	err := restore(ctx, backup, stateTarget, repositoryTarget, "", operations)
	if err == nil || !strings.Contains(err.Error(), "remains pending") {
		t.Fatalf("partial publication error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(stateTarget, pendingRestoreName)); err != nil {
		t.Fatalf("published state is not marked pending: %v", err)
	}
	if _, err := state.Open(ctx, stateTarget); err == nil || !strings.Contains(err.Error(), "incomplete offline restore") {
		t.Fatalf("pending state was usable after rollback failure: %v", err)
	}
	if _, err := os.Stat(repositoryTarget); !os.IsNotExist(err) {
		t.Fatalf("incomplete repository root was published: %v", err)
	}
	stages, err := filepath.Glob(repositoryTarget + ".owngit-restore-*")
	if err != nil || len(stages) != 1 {
		t.Fatalf("preserved repository stages=%v err=%v", stages, err)
	}
	if _, err := os.Stat(filepath.Join(stages[0], pendingRestoreName)); err != nil {
		t.Fatalf("preserved repository stage is not marked pending: %v", err)
	}
	if err := Restore(ctx, backup, stateTarget, repositoryTarget, ""); err == nil || !strings.Contains(err.Error(), "incomplete OwnGit restore") {
		t.Fatalf("retry did not identify preserved incomplete restore: %v", err)
	}
}

func TestRestoreRejectsNonportableRepositoryIDBeforePublishing(t *testing.T) {
	root := t.TempDir()
	backup := filepath.Join(root, "backup")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("admin-password")
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Format: backupFormat, Version: backupVersion, CreatedAt: time.Now().UTC(),
		AccessMode: "open", AdminHash: hash,
		Repositories: []RepositoryManifest{{Name: "Unsupported", CreatedAt: time.Now().UTC(), Empty: true}},
	}
	for _, id := range []string{"foo.git", "con.archive"} {
		manifest.Repositories[0].ID = id
		if err := validateManifest(manifest); err == nil {
			t.Errorf("manifest accepted unsupported repository ID %q", id)
		}
	}
	manifest.Repositories[0].ID = "con.archive"
	file, err := os.OpenFile(filepath.Join(backup, manifestName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(manifest); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	stateTarget := filepath.Join(root, "state-target")
	repositoryTarget := filepath.Join(root, "repository-target")
	if err := Restore(context.Background(), backup, stateTarget, repositoryTarget, ""); err == nil || !strings.Contains(err.Error(), "not portable") {
		t.Fatalf("nonportable manifest error=%v", err)
	}
	assertNoRecoveryOutputOrStages(t, stateTarget, ".owngit-restore-")
	assertNoRecoveryOutputOrStages(t, repositoryTarget, ".owngit-restore-")
}

func TestIncompleteRestoreMarkerBlocksStateOpen(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "pending-state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, pendingRestoreName), []byte("pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Open(context.Background(), directory); err == nil || !strings.Contains(err.Error(), "incomplete offline restore") {
		t.Fatalf("open pending state error=%v", err)
	}
}

func newOldGitExecutable(t *testing.T) (string, string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	marker := filepath.Join(root, "merge-tree-invoked")
	wrapper := filepath.Join(root, "git-old")
	script := "#!/bin/sh\n" +
		"if test \"$1\" = --version; then echo 'git version 2.37.6'; exit 0; fi\n" +
		"for arg in \"$@\"; do\n" +
		"  if test \"$arg\" = merge-tree; then printf invoked > " + recoveryShellQuote(marker) + "; exit 97; fi\n" +
		"done\n" +
		"exec " + recoveryShellQuote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrapper, marker
}

func recoveryShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func recoveryProblemCode(err error) string {
	var problem *pullrequest.Problem
	if errors.As(err, &problem) {
		return problem.Code
	}
	return ""
}

func createRecoveryMergeCommit(t *testing.T, ctx context.Context, runner *gitexec.Runner, repositoryPath string, request *pullrequest.View, intent state.PullRequestMergeIntent) string {
	t.Helper()
	date := strconv.FormatInt(intent.CreatedAt.Unix(), 10) + " +0000"
	environment := []string{
		"GIT_AUTHOR_NAME=OwnGit",
		"GIT_AUTHOR_EMAIL=owngit@localhost",
		"GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=OwnGit",
		"GIT_COMMITTER_EMAIL=owngit@localhost",
		"GIT_COMMITTER_DATE=" + date,
	}
	message := "Merge pull request #" + strconv.FormatInt(request.Number, 10) + ": " + request.Title + "\n"
	result, err := runner.RunWithEnvironment(ctx, "", strings.NewReader(message), environment,
		"--git-dir", repositoryPath, "commit-tree", intent.TreeOID, "-p", intent.TargetOID, "-p", intent.SourceOID)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(result.Stdout))
}

func refExists(t *testing.T, repositoryPath, ref string) bool {
	t.Helper()
	_, err := gitCombined("", "--git-dir", repositoryPath, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func objectExists(t *testing.T, repositoryPath, oid string) bool {
	t.Helper()
	_, err := gitCombined("", "--git-dir", repositoryPath, "cat-file", "-e", oid+"^{object}")
	return err == nil
}

func canonicalTestTarget(t *testing.T, target string) string {
	t.Helper()
	identity, err := absentTarget(target, "test")
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func assertNoRecoveryOutputOrStages(t *testing.T, target, stageInfix string) {
	t.Helper()
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("final destination exists after failure: %s (%v)", target, err)
	}
	matches, err := filepath.Glob(target + stageInfix + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("operation-owned stages remain after ordinary failure: %v", matches)
	}
	backupMatches, err := filepath.Glob(filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+stageInfix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backupMatches) != 0 {
		t.Fatalf("operation-owned backup stages remain after ordinary failure: %v", backupMatches)
	}
}

func newBackupStore(t *testing.T, root string) (*state.Store, *repository.Manager) {
	t.Helper()
	ctx := context.Background()
	stateDir := filepath.Join(root, "source-state")
	repositoriesRoot := filepath.Join(root, "source-repositories")
	if err := os.Mkdir(repositoriesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	adminHash, _ := auth.HashPassword("admin-password")
	if err := store.CompleteSetup(ctx, repositoriesRoot, "open", "", adminHash, false); err != nil {
		t.Fatal(err)
	}
	runner, err := gitexec.New("", filepath.Join(stateDir, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: repositoriesRoot}
	if _, err := manager.Create(ctx, "project", ""); err != nil {
		t.Fatal(err)
	}
	remote, _ := manager.Path("project")
	work := filepath.Join(root, "backup-work")
	runGit(t, "", "init", "--initial-branch=main", work)
	runGit(t, work, "config", "user.name", "Backup Test")
	runGit(t, work, "config", "user.email", "backup@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "file"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "data")
	runGit(t, work, "push", remote, "HEAD:refs/heads/main")
	return store, manager
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	if output, err := gitCombined(directory, arguments...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(arguments, " "), err, output, stderr.Bytes())
	}
	return strings.TrimSpace(string(output))
}

func gitCombined(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	return string(output), err
}

func assertRef(t *testing.T, repositoryPath, ref, want string) {
	t.Helper()
	if got := gitOutput(t, "", "--git-dir", repositoryPath, "rev-parse", "--verify", ref); got != want {
		t.Fatalf("%s=%s, want %s", ref, got, want)
	}
}
