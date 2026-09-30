//go:build darwin || linux

package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Where the file system cannot rename without replacing, a backup is still
// published, never over anything that exists.
func TestPublicationWithoutAnExclusiveRename(t *testing.T) {
	unsupported := errors.New("exclusive rename unsupported")
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	noErr(t, os.Mkdir(stage, 0o700))
	noErr(t, os.WriteFile(filepath.Join(stage, "manifest.json"), []byte("{}"), 0o600))

	full := filepath.Join(root, "full")
	noErr(t, os.Mkdir(full, 0o700))
	noErr(t, os.WriteFile(filepath.Join(full, "sentinel"), []byte("keep"), 0o600))
	file := filepath.Join(root, "file")
	noErr(t, os.WriteFile(file, []byte("keep"), 0o600))
	for _, target := range []string{full, file} {
		if err := renameDirectoryOnce(stage, target, unsupported); err == nil {
			t.Fatalf("published over %s", target)
		}
	}
	if content, err := os.ReadFile(filepath.Join(full, "sentinel")); err != nil || string(content) != "keep" {
		t.Fatalf("existing folder changed: %q %v", content, err)
	}
	if err := renameDirectoryOnce(file, filepath.Join(root, "moved"), unsupported); err != unsupported {
		t.Fatalf("a file was renamed: %v", err)
	}
	target := filepath.Join(root, "published")
	noErr(t, renameDirectoryOnce(stage, target, unsupported))
	if _, err := os.Stat(filepath.Join(target, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}
