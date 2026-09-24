package gitexec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestRepositoryLockGenerationCountsWriteReleases proves that every release
// of the write lock through the returned value advances the generation, and
// that readers do not.
func TestRepositoryLockGenerationCountsWriteReleases(t *testing.T) {
	locks := NewLocks()
	lock := locks.For("project")
	if locks.For("project") != lock || locks.For("other") == lock {
		t.Fatal("For must return one lock per repository")
	}
	if lock.Generation() != 0 {
		t.Fatalf("new lock generation=%d, want 0", lock.Generation())
	}
	lock.RLock()
	lock.RUnlock()
	if !lock.TryRLock() {
		t.Fatal("TryRLock failed on a free lock")
	}
	lock.RUnlock()
	if lock.Generation() != 0 {
		t.Fatalf("readers advanced the generation to %d", lock.Generation())
	}

	lock.Lock()
	lock.Unlock()
	if lock.Generation() != 1 {
		t.Fatalf("after Lock and Unlock generation=%d, want 1", lock.Generation())
	}
	if !lock.TryLock() {
		t.Fatal("TryLock failed on a free lock")
	}
	lock.Unlock()
	if lock.Generation() != 2 {
		t.Fatalf("after TryLock and Unlock generation=%d, want 2", lock.Generation())
	}
	// A caller that only knows the lock as a sync.Locker still releases it
	// through RepositoryLock.Unlock.
	var locker sync.Locker = locks.For("project")
	locker.Lock()
	locker.Unlock()
	if lock.Generation() != 3 {
		t.Fatalf("after sync.Locker Unlock generation=%d, want 3", lock.Generation())
	}
}

// TestRepositoryLockGenerationChangesBeforeReaders proves that a reader
// waiting for a writer observes the writer's generation once it gets the lock.
func TestRepositoryLockGenerationChangesBeforeReaders(t *testing.T) {
	lock := NewLocks().For("project")
	lock.Lock()
	observed := make(chan uint64)
	go func() {
		lock.RLock()
		defer lock.RUnlock()
		observed <- lock.Generation()
	}()
	lock.Unlock()
	if generation := <-observed; generation != 1 {
		t.Fatalf("reader after the writer saw generation %d, want 1", generation)
	}
}

// TestNoCodeReachesTheEmbeddedRepositoryMutex scans the module for a selector
// of RWMutex on anything but the sync package. Unlocking the embedded mutex
// of a RepositoryLock, or passing its address on, would skip the generation
// step that keeps cached ref snapshots current.
func TestNoCodeReachesTheEmbeddedRepositoryMutex(t *testing.T) {
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	self, err := filepath.Abs("locks.go")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	scanned := 0
	err = filepath.WalkDir(moduleRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); path != moduleRoot && (strings.HasPrefix(name, ".") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || path == self {
			return nil
		}
		parsed, err := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanned++
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "RWMutex" {
				return true
			}
			if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "sync" {
				return true
			}
			t.Errorf("%s: reaches an embedded RWMutex; use the RepositoryLock itself", files.Position(selector.Pos()))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned %d Go files, want the whole module", scanned)
	}
}
