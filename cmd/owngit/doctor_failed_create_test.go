package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"owngit/internal/gitexec"
	"owngit/internal/repository"
	"owngit/internal/state"
)

func TestServerDiagnosisIgnoresFailedCreationPreservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := t.TempDir()
	store, err := state.Open(ctx, filepath.Join(base, "state"))
	noErr(t, err)
	t.Cleanup(func() { _ = store.Close() })
	runner, err := gitexec.New("", filepath.Join(base, "runtime"))
	noErr(t, err)
	root := filepath.Join(base, "repositories")
	noErr(t, os.Mkdir(root, 0o700))
	manager := &repository.Manager{Store: store, Git: runner, Locks: gitexec.NewLocks(), Root: root}
	diagnosis := serverDiagnosis(store.Dir(), "127.0.0.1:48933", false, func(context.Context) (string, []string, error) {
		root, err := manager.CanonicalStorageRoot()
		return root, []string{"sample"}, err
	})
	before := diagnosis(ctx)
	noErr(t, store.Exec(ctx, `CREATE TRIGGER refuse_creation BEFORE INSERT ON repositories BEGIN SELECT RAISE(ABORT,'recording refused'); END`))
	if _, err := manager.Create(ctx, "fresh", ""); err == nil || !strings.Contains(err.Error(), "may be removed") {
		t.Fatalf("creation failure did not preserve its empty tree: %v", err)
	}
	noErr(t, store.Exec(ctx, `DROP TRIGGER refuse_creation`))
	preserved, err := os.ReadDir(filepath.Join(root, ".owngit-failed-create"))
	if err != nil || len(preserved) != 1 {
		t.Fatalf("preservation folders=%v err=%v", preserved, err)
	}
	if after := diagnosis(ctx); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed-creation folder changed actual checkup: before=%+v after=%+v", before, after)
	}
}
