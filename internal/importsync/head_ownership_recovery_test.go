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
	if run.RefsDivergent != 1 || f.git(f.destinationPath(), "symbolic-ref", "HEAD") != "refs/heads/release" {
		t.Fatal("unrecorded ownership was not reported conservatively")
	}
	noErr(t, f.manager.SetDefaultBranch(ctx, "project", "main"))
	run, err = f.refresh()
	noErr(t, err)
	intent, exists, err := f.store.CompletedImportIntentForRun(ctx, run.ID)
	noErr(t, err)
	if !exists || intent.HeadOwned {
		t.Fatal("choosing a branch granted importer ownership")
	}
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/release")
	run, err = f.refresh()
	noErr(t, err)
	if run.RefsDivergent != 1 || f.git(f.destinationPath(), "symbolic-ref", "HEAD") != "refs/heads/main" {
		t.Fatal("later source HEAD replaced the independent default choice")
	}
	t.Log("Choosing the default branch works, but does not restore automatic source HEAD following.")
}

func TestRepositoryWriterRenameDoesNotGrantHEADOwnership(t *testing.T) {
	f := killedPublicationFixture(t, "head-locked")
	ctx := context.Background()
	pending, err := f.store.PendingImportIntents(ctx, "project")
	noErr(t, err)
	if len(pending) != 1 || pending[0].HeadOwned {
		t.Fatal("fixture did not stop before the importer HEAD write")
	}
	root := f.destinationPath()
	// A repository writer adopts the original lock file without touching
	// private state. Rename preserves every recorded file fingerprint.
	noErr(t, os.Rename(filepath.Join(root, "HEAD.lock"), filepath.Join(root, "HEAD")))
	noErr(t, f.service.Reconcile(ctx))
	intent, exists, err := f.store.ImportIntent(ctx, pending[0].ID)
	noErr(t, err)
	if !exists || intent.Status != state.ImportIntentComplete {
		t.Fatalf("exact publication was not reconciled: %+v", intent)
	}
	t.Logf("outside rename recovered status=%s HeadOwned=%v", intent.Status, intent.HeadOwned)
	f.git(f.source, "symbolic-ref", "HEAD", "refs/heads/main")
	run, err := f.refresh()
	noErr(t, err)
	actual := f.git(root, "symbolic-ref", "HEAD")
	t.Logf("next refresh HEAD=%s divergent=%d", actual, run.RefsDivergent)
	if intent.HeadOwned || actual != "refs/heads/release" || run.RefsDivergent == 0 {
		t.Fatal("repository-only writer manufactured importer HEAD ownership")
	}
}
