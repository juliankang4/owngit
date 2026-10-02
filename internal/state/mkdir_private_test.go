package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMkdirPrivateReportsOnlyCreationCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	noErr(t, os.Mkdir(path, 0o700))
	err := MkdirPrivate(path)
	if !errors.Is(err, ErrPrivateDirectoryExists) {
		t.Fatalf("existing directory error=%v", err)
	}
	if errors.Is(err, os.ErrExist) {
		t.Fatalf("creation collision leaked the ambiguous filesystem alias: %v", err)
	}
}
