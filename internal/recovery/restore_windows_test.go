//go:build windows

package recovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
)

// While restore works in its stages, neither stage nor a folder on the way
// to them can be renamed, so no other folder can take a stage's place; the
// restore then publishes both.
func TestWindowsRestoreStagesCannotBeRenamedWhileInUse(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, manager := newBackupStore(t, root)
	defer store.Close()
	backup := filepath.Join(root, "backup")
	noErr(t, Create(ctx, store, manager, backup))
	parent := filepath.Join(root, "restored")
	noErr(t, os.Mkdir(parent, 0o700))
	stateTarget := canonicalTestTarget(t, filepath.Join(parent, "state"))
	repositoryTarget := canonicalTestTarget(t, filepath.Join(parent, "repositories"))
	operations := defaultRestoreOperations()
	prepare := operations.prepareExisting
	var renamed []string
	operations.prepareExisting = func(ctx context.Context, manager *repository.Manager, hookRuntime *gitexec.Runner) error {
		stages, err := filepath.Glob(filepath.Join(parent, "*.owngit-restore-*"))
		if err != nil || len(stages) != 2 {
			t.Errorf("stages=%v err=%v", stages, err)
		}
		for _, path := range append(stages, parent) {
			if os.Rename(path, path+"-moved") == nil {
				renamed = append(renamed, path)
			}
		}
		return prepare(ctx, manager, hookRuntime)
	}
	noErr(t, restore(ctx, backup, stateTarget, repositoryTarget, "", operations))
	if len(renamed) != 0 {
		t.Fatalf("renamed while restore used them: %v", renamed)
	}
	for _, path := range []string{stateTarget, repositoryTarget} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
}
