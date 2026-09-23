package recovery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owngit/internal/auth"
	"owngit/internal/state"
)

// Only unreleased development builds wrote direct-review records. A backup
// never drops them silently, and a restore refuses a manifest that names them.

func TestBackupRefusesPortableDirectReviewResidue(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	// A machine-local credential was never portable and does not block backup.
	if err := store.Exec(ctx, `INSERT INTO direct_review_credentials(id,repository_id,label,value,created_at,updated_at)
		VALUES('credential','project','label','provider-token',1,1)`); err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(root, "allowed")
	if err := Create(ctx, store, manager, allowed); err != nil {
		t.Fatalf("backup with machine-local residue: %v", err)
	}
	assertBackupOmits(t, allowed, "provider-token")
	restoredState := canonicalTestTarget(t, filepath.Join(root, "restored-state"))
	if err := Restore(ctx, allowed, restoredState, canonicalTestTarget(t, filepath.Join(root, "restored-repositories")), ""); err != nil {
		t.Fatalf("restore of a backup taken beside residue: %v", err)
	}

	if err := store.Exec(ctx, `INSERT INTO direct_review_repository_settings(repository_id,configuration_version,protocol,endpoint,model,
		authentication_mode,provider_limits_json,repository_limits_json,instruction_version,authority_epoch,created_at,updated_at)
		VALUES('project',1,'openai_responses','https://provider.invalid','model','none','{}','{}','v','`+strings.Repeat("e", 32)+`',1,1)`); err != nil {
		t.Fatal(err)
	}
	refused := filepath.Join(root, "refused")
	if err := Create(ctx, store, manager, refused); !errors.Is(err, state.ErrDirectReviewRecords) {
		t.Fatalf("backup with portable residue err=%v", err)
	}
	assertNoRecoveryOutputOrStages(t, refused, ".owngit-backup-")
}

func TestRestoreRefusesManifestWithDirectReviewRecords(t *testing.T) {
	hash, err := auth.HashPassword("admin-password")
	noErr(t, err)
	for _, field := range []string{"direct_review_settings", "direct_review_task_contexts", "direct_review_requests"} {
		t.Run(field, func(t *testing.T) {
			root := t.TempDir()
			backup := filepath.Join(root, "backup")
			noErr(t, os.Mkdir(backup, 0o700))
			manifestPath := filepath.Join(backup, manifestName)
			writeManifestFile(t, manifestPath, Manifest{Format: backupFormat, Version: backupVersion, CreatedAt: time.Now().UTC(), AccessMode: "open", AdminHash: hash})
			content, err := os.ReadFile(manifestPath)
			noErr(t, err)
			content = bytes.Replace(content, []byte(`"format"`), []byte(`"`+field+`": [],
  "format"`), 1)
			noErr(t, os.WriteFile(manifestPath, content, 0o600))
			const want = "backup contains direct-review records written by an unreleased development build; this build cannot restore them"
			stateTarget, repositoryTarget := filepath.Join(root, "state"), filepath.Join(root, "repositories")
			if err := Restore(context.Background(), backup, stateTarget, repositoryTarget, ""); err == nil || err.Error() != want {
				t.Fatalf("restore error=%v", err)
			}
			for _, target := range []string{stateTarget, repositoryTarget} {
				assertNoRecoveryOutputOrStages(t, target, ".owngit-restore-")
			}
		})
	}
}

func assertBackupOmits(t *testing.T, backup, value string) {
	t.Helper()
	err := filepath.WalkDir(backup, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err == nil && bytes.Contains(content, []byte(value)) {
			t.Errorf("backup file %s contains %q", path, value)
		}
		return err
	})
	noErr(t, err)
}
