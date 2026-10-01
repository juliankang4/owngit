package recovery

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"owngit/internal/gitexec"
)

// exactBackupRunner creates a portable fixture without macOS precomposition,
// as Git does on Linux. Verification and restore use the normal runner.
type exactBackupRunner struct{ *gitexec.Runner }

func (r exactBackupRunner) Run(ctx context.Context, dir string, input io.Reader, args ...string) (gitexec.Result, error) {
	return r.Runner.Run(ctx, dir, input, append([]string{"-c", "core.precomposeUnicode=false"}, args...)...)
}

func TestBackupVerifyRestoreKeepsDecomposedRefs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	remote, err := manager.Path("project")
	noErr(t, err)
	oid := gitOutput(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main")
	refs := []string{"refs/heads/\u1112\u1161\u11ab\u1100\u1173\u11af-follow", "refs/tags/cafe\u0301-tag"}
	runGit(t, "", "--git-dir", remote, "config", "core.precomposeUnicode", "true")
	for _, ref := range refs {
		runGit(t, "", "-c", "core.precomposeUnicode=false", "--git-dir", remote, "update-ref", ref, oid)
	}
	runner := manager.Git
	checkManifest := func(t *testing.T, backup string) {
		t.Helper()
		manifest, err := readManifest(filepath.Join(backup, "manifest.json"))
		noErr(t, err)
		if len(manifest.Repositories) != 1 {
			t.Fatalf("repositories=%+v", manifest.Repositories)
		}
		for _, ref := range refs {
			found := false
			for _, actual := range manifest.Repositories[0].Refs {
				found = found || actual.Name == ref && actual.OID == oid
			}
			if !found {
				t.Fatalf("exact ref %q absent from manifest: %+v", ref, manifest.Repositories[0].Refs)
			}
		}
	}
	t.Run("capture", func(t *testing.T) {
		backup := filepath.Join(root, "captured-backup")
		noErr(t, Create(ctx, store, manager, backup))
		checkManifest(t, backup)
	})
	t.Run("portable backup", func(t *testing.T) {
		backup := filepath.Join(root, "portable-backup")
		_, err := create(ctx, store, manager, exactBackupRunner{runner}, backup, manifestLimit)
		noErr(t, err)
		checkManifest(t, backup)
		verification, err := Verify(ctx, backup, root, "")
		if err != nil || !verification.Verified || verification.Database != VerifyPassed || len(verification.Repositories) != 1 || verification.Repositories[0].Status != VerifyPassed {
			t.Fatalf("verification=%+v err=%v", verification, err)
		}
		stateTarget := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
		repositoryTarget := canonicalTestTarget(t, filepath.Join(root, "restored-repositories"))
		noErr(t, Restore(ctx, backup, stateTarget, repositoryTarget, ""))
		restored := filepath.Join(repositoryTarget, "project.git")
		result, err := runner.Run(ctx, restored, nil, "for-each-ref", "--format=%(refname) %(objectname)")
		noErr(t, err)
		for _, ref := range refs {
			if !strings.Contains(string(result.Stdout), ref+" "+oid+"\n") {
				t.Fatalf("restored ref %q missing: %q", ref, result.Stdout)
			}
		}
		result, err = runner.Run(ctx, restored, nil, "show", refs[0]+":file")
		if err != nil || string(result.Stdout) != "data" {
			t.Fatalf("restored branch content=%q err=%v", result.Stdout, err)
		}
	})
}
