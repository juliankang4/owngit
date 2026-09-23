package publishdir

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameMovesDirectoryAndRefusesNonEmptyDestination(t *testing.T) {
	root := t.TempDir()
	staged := stagedDirectory(t, root, "staged")
	published := filepath.Join(root, "published")
	if err := Rename(context.Background(), staged, published); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(published, "hooks", "update")); err != nil || string(content) != "staged\n" {
		t.Fatalf("published content=%q err=%v", content, err)
	}
	if _, err := os.Lstat(staged); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staged path after publication: %v", err)
	}

	second := stagedDirectory(t, root, "second")
	err := Rename(context.Background(), second, published)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("publication onto a non-empty destination err=%v", err)
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || linkErr.Old != second || linkErr.New != published {
		t.Fatalf("error is not the rename link error: %#v", err)
	}
	if content, err := os.ReadFile(filepath.Join(published, "hooks", "update")); err != nil || string(content) != "staged\n" {
		t.Fatalf("existing destination changed: content=%q err=%v", content, err)
	}
}

// stagedDirectory creates root/name/hooks/update like a new bare repository.
func stagedDirectory(t *testing.T, root, name string) string {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(directory, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "hooks", "update"), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return directory
}
