package gitexec

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestUngatedLockReferencesStayWithinStorageVerificationPaths(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("module root not found")
		}
		root = parent
	}

	allowed := map[string]int{
		"internal/repository/delete.go":           1,
		"internal/repository/manager.go":          1,
		"internal/repository/preparation.go":      1,
		"internal/repository/storage_identity.go": 2,
	}
	references := make(map[string][]token.Position)
	for path := range allowed {
		references[path] = nil
	}
	positions := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(positions, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		ast.Inspect(file, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok && selector.Sel.Name == "LockContextUngated" {
				references[relative] = append(references[relative], positions.Position(selector.Sel.Pos()))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	files := make([]string, 0, len(references))
	for path := range references {
		files = append(files, path)
	}
	slices.Sort(files)
	for _, path := range files {
		found, want := references[path], allowed[path]
		if len(found) != want {
			line := 1
			if len(found) > want {
				line = found[want].Line
			}
			t.Errorf("%s:%d: LockContextUngated skips the storage check, so verify storage before writing and then update the list (found %d references, want %d).", path, line, len(found), want)
		}
	}
}
