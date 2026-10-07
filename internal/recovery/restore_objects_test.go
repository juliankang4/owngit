package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A valid pack and bundle digest cannot hide a missing reachable object.
func TestRestoreRejectsMissingObjects(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	backup := filepath.Join(root, "backup")
	_, err := CreateWithReport(ctx, store, manager, backup)
	noErr(t, err)
	remote, err := manager.Path("project")
	noErr(t, err)
	commit := gitOutput(t, remote, "--git-dir", ".", "rev-parse", "HEAD")
	tree := gitOutput(t, remote, "--git-dir", ".", "rev-parse", "HEAD^{tree}")
	// The pack contains the commit and tree, but not the tree's blob.
	pack, err := manager.Git.Run(ctx, remote, strings.NewReader(commit+"\n"+tree+"\n"), "--git-dir", ".", "pack-objects", "--stdout")
	noErr(t, err)
	bundlePath := filepath.Join(backup, "repositories", "project.bundle")
	bundle, err := os.ReadFile(bundlePath)
	noErr(t, err)
	end := bytes.Index(bundle, []byte("\n\n"))
	if end < 0 {
		t.Fatal("bundle has no pack boundary")
	}
	bundle = append(bundle[:end+2], pack.Stdout...)
	noErr(t, os.WriteFile(bundlePath, bundle, 0o600))
	input, err := openBackupInput(backup)
	noErr(t, err)
	manifest, _, err := input.readManifest()
	input.Close()
	noErr(t, err)
	digest := sha256.Sum256(bundle)
	manifest.Repositories[0].SHA256 = hex.EncodeToString(digest[:])
	writeManifestFile(t, filepath.Join(backup, manifestName), manifest)
	stateTarget, repoTarget := filepath.Join(root, "restored-state"), filepath.Join(root, "restored-repositories")
	report, err := RestoreWithReport(ctx, backup, stateTarget, repoTarget, "")
	if err == nil || !strings.Contains(err.Error(), `repository "project"`) || len(report.ObjectWarnings) != 0 {
		t.Fatalf("missing-object restore report=%+v err=%v", report, err)
	}
	assertNoRecoveryOutputOrStages(t, stateTarget, ".owngit-restore-")
	assertNoRecoveryOutputOrStages(t, repoTarget, ".owngit-restore-")
}
