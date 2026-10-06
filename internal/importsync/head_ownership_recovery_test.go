package importsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/state"
)

func TestChoosingDefaultBranchDoesNotRestoreUnrecordedHEADOwnership(t *testing.T) {
	f := killedPublicationFixture(t, "after-head")
	ctx := context.Background()
	noErr(t, f.service.Reconcile(ctx))
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/main")
	run, err := f.refresh()
	noErr(t, err)
	require(t, run.RefsDivergent == 1 && f.git(f.destinationPath(), "symbolic-ref", "HEAD") == "refs/heads/release",
		"unrecorded ownership was not reported conservatively")
	noErr(t, f.manager.SetDefaultBranch(ctx, "project", "main"))
	run, err = f.refresh()
	noErr(t, err)
	intent, exists, err := f.store.CompletedImportIntentForRun(ctx, run.ID)
	noErr(t, err)
	require(t, exists && !intent.HeadOwned, "choosing a branch granted importer ownership")
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
	run, err = f.refresh()
	noErr(t, err)
	require(t, run.RefsDivergent == 1 && f.git(f.destinationPath(), "symbolic-ref", "HEAD") == "refs/heads/main",
		"later source HEAD replaced the independent default choice")
}

func TestRepositoryWriterRenameDoesNotGrantHEADOwnership(t *testing.T) {
	f := killedPublicationFixture(t, "head-locked")
	ctx := context.Background()
	pending, err := f.store.PendingImportIntents(ctx, "project")
	noErr(t, err)
	require(t, len(pending) == 1 && !pending[0].HeadOwned, "fixture did not stop before the importer HEAD write")
	root := f.destinationPath()
	// A repository writer adopts the original lock file without touching
	// private state. Rename preserves every recorded file fingerprint.
	noErr(t, os.Rename(filepath.Join(root, "HEAD.lock"), filepath.Join(root, "HEAD")))
	noErr(t, f.service.Reconcile(ctx))
	intent, exists, err := f.store.ImportIntent(ctx, pending[0].ID)
	noErr(t, err)
	require(t, exists && intent.Status == state.ImportIntentComplete,
		"exact publication was not reconciled: %+v", intent)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/main")
	run, err := f.refresh()
	noErr(t, err)
	require(t, !intent.HeadOwned && f.git(root, "symbolic-ref", "HEAD") == "refs/heads/release" &&
		run.RefsDivergent != 0, "repository-only writer manufactured importer HEAD ownership")
}
