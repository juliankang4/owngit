package state

import (
	"os"
	"path/filepath"
	"testing"
)

// A Destination is named in its resolved parent, and its stage is a new
// folder private to this account: an entry that already has the stage's
// name is refused and left alone, not adopted.
func TestDestinationStageIsNewAndPrivate(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	noErr(t, os.Mkdir(parent, 0o700))
	destination, err := OpenDestination(filepath.Join(parent, "restored"))
	noErr(t, err)
	defer destination.Close()
	resolved, err := destination.parent.Stat()
	noErr(t, err)
	want, err := os.Stat(parent)
	noErr(t, err)
	if !os.SameFile(resolved, want) || filepath.Base(destination.Path) != "restored" || filepath.Dir(destination.Path) != destination.parent.Name() {
		t.Fatalf("Path=%s, parent %s", destination.Path, destination.parent.Name())
	}

	planted := destination.Path + ".planted"
	noErr(t, os.Mkdir(planted, 0o700))
	noErr(t, os.WriteFile(filepath.Join(planted, "kept"), []byte("kept"), 0o600))
	if _, err := destination.CreateStage("restored.planted"); err == nil {
		t.Fatal("an existing folder was taken as the stage")
	}
	if kept, err := os.ReadFile(filepath.Join(planted, "kept")); err != nil || string(kept) != "kept" {
		t.Fatalf("the existing folder changed: %q %v", kept, err)
	}

	stage, err := destination.CreateStage("restored.stage")
	noErr(t, err)
	if stage != destination.Path+".stage" {
		t.Fatalf("stage=%s", stage)
	}
	private, err := OpenPrivateFolderIn(destination.parent, filepath.Base(stage))
	noErr(t, err)
	private.Close()
	destination.ReleaseStage()
	noErr(t, os.Rename(stage, destination.Path))
}
