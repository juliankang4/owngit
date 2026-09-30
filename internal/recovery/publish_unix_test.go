//go:build darwin || linux

package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Where the file system cannot rename without replacing, a backup is still
// published, and never over anything that exists, an empty folder
// included: the new folder is made with mkdir, which fails when anything
// is at its name. (A folder that appears between mkdir and opening it can
// come only from a process that may write in the parent; that window has
// no deterministic test.)
func TestPublicationWithoutAnExclusiveRename(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	noErr(t, os.MkdirAll(filepath.Join(stage, "repositories"), 0o700))
	noErr(t, os.WriteFile(filepath.Join(stage, "repositories", "project.bundle"), []byte("bundle"), 0o600))
	noErr(t, os.WriteFile(filepath.Join(stage, manifestName), []byte("{}"), 0o600))
	// Companions of the file system, which this one does not move with
	// their files, still arrive.
	for _, companion := range []string{"._repositories", "._" + manifestName} {
		noErr(t, os.WriteFile(filepath.Join(stage, companion), []byte("attributes"), 0o600))
	}

	empty := filepath.Join(root, "empty")
	noErr(t, os.Mkdir(empty, 0o755))
	full := filepath.Join(root, "full")
	noErr(t, os.Mkdir(full, 0o700))
	noErr(t, os.WriteFile(filepath.Join(full, "sentinel"), []byte("keep"), 0o600))
	file := filepath.Join(root, "file")
	noErr(t, os.WriteFile(file, []byte("keep"), 0o600))
	for _, target := range []string{empty, full, file} {
		before, err := os.Lstat(target)
		noErr(t, err)
		if err := moveIntoNewFolder(stage, target); err == nil {
			t.Fatalf("published over %s", target)
		}
		after, err := os.Lstat(target)
		if err != nil || !os.SameFile(before, after) || after.Mode() != before.Mode() {
			t.Fatalf("%s was replaced or changed: %v", target, err)
		}
	}
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Fatalf("the empty folder received %d entries: %v", len(entries), err)
	}
	if err := moveIntoNewFolder(file, filepath.Join(root, "moved")); err == nil {
		t.Fatal("a file was published")
	}

	target := filepath.Join(root, "published")
	noErr(t, moveIntoNewFolder(stage, target))
	for _, name := range []string{manifestName, filepath.Join("repositories", "project.bundle"), "._repositories", "._" + manifestName} {
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage still there: %v", err)
	}
}

// On a file system with the exclusive rename and attributes of its own,
// the checks that a restore makes first pass and leave nothing behind.
func TestRestoreFolderCheckLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	noErr(t, requireExclusiveRename(dir))
	noErr(t, requireAttributesInPlace(dir))
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("%d entries left: %v", len(entries), err)
	}
}
